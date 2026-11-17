package auth

import (
	"encoding/json"
	"fmt"
	"html/template"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// public 是不需要认证的路径：健康检查、登录页及图标、登录、查询当前认证方式、清除会话。
// 写请求仍然要过同源检查，登录本身还受失败限速约束。
func public(path string) bool {
	switch path {
	case "/api/health", "/login", "/favicon.svg", "/api/auth/login", "/api/auth/me", "/api/auth/logout":
		return true
	}
	return false
}

// Middleware 是所有请求的入口：统一加安全响应头、拦截跨站写请求，
// 然后放行公开路径和已登录请求；其余 /api/* 返回 401，页面跳转到 /login。
// 本机回环没有例外，所有来源都必须先登录。
func (g *Guard) Middleware(next http.Handler) http.Handler {
	lim := newLimiter()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", g.loginPage)
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) { g.login(w, r, lim) })
	mux.HandleFunc("POST /api/auth/password", g.changePassword)
	mux.HandleFunc("POST /api/auth/logout", g.logout)
	mux.HandleFunc("GET /api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		mode := g.mode(r)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":            true,
			"enabled":       true,
			"authenticated": mode != "none",
			"mode":          mode,
		})
	})
	mux.Handle("/", next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		if !sameOrigin(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "拒绝跨站请求：写操作只能从 reeve 自己的页面发起"})
			return
		}
		if public(r.URL.Path) || g.authorized(r) {
			mux.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "需要登录：打开登录页输入密码"})
			return
		}
		http.Redirect(w, r, "/login", http.StatusFound)
	})
}

// sameOrigin 判断写请求是不是同源：带 Origin 时它的主机和端口必须与请求的 Host 完全一致；
// 没带 Origin 时，浏览器标了 Sec-Fetch-Site: cross-site 也拒绝。
func sameOrigin(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
	}
	return !strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// login 用密码换会话。只接受 JSON {"password":"…"} 或表单字段 password；
// 同一个来源 IP 5 分钟内失败 10 次后返回 429。
// 网页用 Set-Cookie 保存会话，API 客户端用响应里的 token 作为 Bearer。
func (g *Guard) login(w http.ResponseWriter, r *http.Request, lim *limiter) {
	ip, now := clientIP(r), time.Now()
	if d := lim.blocked(ip, now); d > 0 {
		w.Header().Set("Retry-After", fmt.Sprint(int(d.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{
			"error": fmt.Sprintf("密码试错次数太多，请 %s后再试", roundUp(d))})
		return
	}
	in, ok := readPasswordRequest(w, r, 4<<10)
	if !ok {
		return
	}
	valid, err := VerifyPassword(g.DataDir, in.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "服务器还没有可用的登录密码：请在服务器上运行 reeve passwd"})
		return
	}
	if !valid {
		lim.fail(ip, now)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "密码不对"})
		return
	}
	lim.ok(ip)
	token, expires, err := g.newSession()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "会话密钥不可用：请在服务器上运行 reeve passwd 重新生成"})
		return
	}
	c := newCookie(token, g.ttl())
	c.Secure = secureRequest(r)
	http.SetCookie(w, c)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "token": token, "expires_at": expires.UTC().Format(time.RFC3339)})
}

// changePassword 修改密码：验证当前密码后写入新哈希，轮换会话密钥，
// 并给当前调用方签发新会话；其他设备上的旧会话立即失效。
func (g *Guard) changePassword(w http.ResponseWriter, r *http.Request) {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	body := http.MaxBytesReader(w, r.Body, 8<<10)
	var in struct {
		Current string `json:"current_password"`
		Next    string `json:"new_password"`
	}
	var err error
	switch media {
	case "application/json":
		err = json.NewDecoder(body).Decode(&in)
	case "application/x-www-form-urlencoded", "":
		r.Body = body
		if err = r.ParseForm(); err == nil {
			in.Current = r.PostForm.Get("current_password")
			in.Next = r.PostForm.Get("new_password")
		}
	default:
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{
			"error": "请求必须是 JSON 或表单"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式不对"})
		return
	}
	valid, err := VerifyPassword(g.DataDir, in.Current)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "服务器还没有可用的登录密码：请在服务器上运行 reeve passwd"})
		return
	}
	if !valid {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "当前密码不对"})
		return
	}
	if err := SetPassword(g.DataDir, in.Next); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	token, expires, err := g.newSession()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "密码已修改，但新会话签发失败；请重新登录"})
		return
	}
	c := newCookie(token, g.ttl())
	c.Secure = secureRequest(r)
	http.SetCookie(w, c)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "token": token, "expires_at": expires.UTC().Format(time.RFC3339)})
}

func readPasswordRequest(w http.ResponseWriter, r *http.Request, limit int64) (struct {
	Password string `json:"password"`
}, bool) {
	var in struct {
		Password string `json:"password"`
	}
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	body := http.MaxBytesReader(w, r.Body, limit)
	var err error
	switch media {
	case "application/json":
		err = json.NewDecoder(body).Decode(&in)
	case "application/x-www-form-urlencoded", "":
		r.Body = body
		if err = r.ParseForm(); err == nil {
			in.Password = r.PostForm.Get("password")
		}
	default:
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{
			"error": "请求必须是 JSON（{\"password\":\"…\"}）或表单"})
		return in, false
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "请求格式不对：应该是 {\"password\":\"…\"}"})
		return in, false
	}
	if strings.TrimSpace(in.Password) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "密码不能为空"})
		return in, false
	}
	return in, true
}

// logout 清除会话 Cookie，返回 204。不要求已登录：Cookie 已过期或密码已修改时也应该能退出。
// Bearer 令牌是无状态的，退出登录时由客户端自行丢弃；要一次注销所有设备请修改密码。
func (g *Guard) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   secureRequest(r),
	})
	w.WriteHeader(http.StatusNoContent)
}

func roundUp(d time.Duration) string {
	if d >= time.Minute {
		return fmt.Sprintf("%d 分钟", int((d+time.Minute-1)/time.Minute))
	}
	return fmt.Sprintf("%d 秒", int((d+time.Second-1)/time.Second))
}

func (g *Guard) loginPage(w http.ResponseWriter, r *http.Request) {
	if g.authorized(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'")
	_ = loginTmpl.Execute(w, map[string]bool{"Insecure": !secureRequest(r)})
}

// ---------- 登录限速 ----------

// 同一个来源 IP 在 loginWindow 内失败 loginMaxFails 次后返回 429，
// 直到最早的一次失败滑出窗口。
const (
	loginWindow   = 5 * time.Minute
	loginMaxFails = 10
	// maxLimiterEntries 是限速表的 IP 上限；超过就清表，见 limiter.fail。
	maxLimiterEntries = 10000
)

type limiter struct {
	mu  sync.Mutex
	ips map[string][]time.Time
}

func newLimiter() *limiter { return &limiter{ips: map[string][]time.Time{}} }

func (l *limiter) blocked(ip string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	fails := l.prune(ip, now)
	if len(fails) < loginMaxFails {
		return 0
	}
	return fails[0].Add(loginWindow).Sub(now)
}

func (l *limiter) fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ips[ip] = append(l.prune(ip, now), now)
	// 防止被刷爆内存：达到上限时先把整张表里过期的条目清掉；如果清完还是满的
	// （大量新鲜 IP），直接清空整表。内存安全优先于精确计数，宁可暂时少限速几个 IP。
	// 只有达到上限时才全表扫描，不会变成每个请求 O(n)。
	if len(l.ips) >= maxLimiterEntries {
		for k, v := range l.ips {
			if len(v) == 0 || now.Sub(v[len(v)-1]) > loginWindow {
				delete(l.ips, k)
			}
		}
		if len(l.ips) >= maxLimiterEntries {
			l.ips = make(map[string][]time.Time)
		}
	}
}

func (l *limiter) ok(ip string) {
	l.mu.Lock()
	delete(l.ips, ip)
	l.mu.Unlock()
}

// prune 丢掉滑出窗口的失败记录，返回剩下的（调用方持有锁）。
func (l *limiter) prune(ip string, now time.Time) []time.Time {
	fails := l.ips[ip]
	cut := now.Add(-loginWindow)
	keep := fails[:0]
	for _, t := range fails {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(l.ips, ip)
		return nil
	}
	l.ips[ip] = keep
	return keep
}

// clientIP 用连接的对端地址；只有对端是本机（前面套了 Caddy/Nginx）时
// 才信 X-Forwarded-For / X-Real-IP。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
		if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
			return xr
		}
	}
	return host
}

// 登录页：暖象牙白、极简、无渐变，和管理页面的风格一致。
var loginTmpl = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>登录 · reeve</title><link rel="icon" href="/favicon.svg" type="image/svg+xml">
<style>
:root{--bg:#faf9f5;--card:#ffffff;--ink:#1a1915;--muted:#69675f;--line:#e6e4dd;--accent:#2f7d52;--warn:#9a6410;--err:#b4433a}
@media (prefers-color-scheme:dark){:root{--bg:#262624;--card:#2b2a27;--ink:#f5f4ef;--muted:#9e9c94;--line:#3d3c38;--accent:#62b486;--warn:#deb36d;--err:#e9918a}}
*{box-sizing:border-box}body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:var(--bg);color:var(--ink);font:15px/1.6 -apple-system,BlinkMacSystemFont,"PingFang SC","Hiragino Sans GB","Microsoft YaHei","Segoe UI",sans-serif}
main{width:min(380px,calc(100vw - 32px));background:var(--card);border:1px solid var(--line);border-radius:16px;padding:32px 28px}
.brand{font-size:18px;font-weight:600;margin-bottom:22px}
label{display:block;font-size:13px;color:var(--muted);margin:14px 0 6px}
input{width:100%;padding:10px 12px;border:1px solid var(--line);border-radius:10px;background:var(--bg);color:var(--ink);font:13px/1.6 ui-monospace,SFMono-Regular,Menlo,monospace;outline:none}
input:focus{border-color:var(--accent)}
button{width:100%;margin-top:22px;padding:10px 12px;border:0;border-radius:10px;background:var(--ink);color:var(--card);font:inherit;font-weight:600;cursor:pointer}
button:disabled{opacity:.6;cursor:default}
.err{color:var(--err);font-size:13px;min-height:20px;margin-top:12px}
.warn{color:var(--warn);font-size:12px;border:1px solid var(--line);border-radius:10px;padding:8px 10px;margin-top:18px}
.foot{color:var(--muted);font-size:12px;margin-top:14px}
</style></head><body><main>
<div class="brand">reeve</div>
<form id="f" method="post" action="/api/auth/login">
<label for="p">密码</label><input id="p" name="password" type="password" autocomplete="current-password" required autofocus>
<button id="b" type="submit">登录</button><div class="err" id="e" role="alert"></div>
</form>
{{if .Insecure}}<div class="warn">当前是明文 HTTP 连接，密码会在网络上明文传输。建议只在可信的本机或局域网使用，或配置 HTTPS。</div>{{end}}
<div class="foot">初始密码在服务器首次启动时打印；忘了可以在服务器上运行 <code>reeve passwd</code> 重设。</div>
</main><script>
const f=document.getElementById('f'),b=document.getElementById('b'),e=document.getElementById('e');
f.addEventListener('submit',async(ev)=>{ev.preventDefault();b.disabled=true;e.textContent='';
try{const r=await fetch('/api/auth/login',{method:'POST',headers:{'Content-Type':'application/json'},credentials:'same-origin',
body:JSON.stringify({password:f.password.value})});
if(r.ok){location.replace('/');return}const d=await r.json().catch(()=>({}));e.textContent=d.error||('登录失败（'+r.status+'）')}
catch(_){e.textContent='连不上服务器'}b.disabled=false});
</script></body></html>`))
