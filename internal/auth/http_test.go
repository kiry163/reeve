package auth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newAuthServer(t *testing.T, g *Guard) *httptest.Server {
	t.Helper()
	app := http.NewServeMux()
	app.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("app")) })
	app.HandleFunc("GET /api/bots", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("bots")) })
	app.HandleFunc("POST /api/bots", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("created")) })
	srv := httptest.NewServer(g.Middleware(app))
	t.Cleanup(srv.Close)
	return srv
}

func doReq(t *testing.T, method, url, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestLoginFlow(t *testing.T) {
	g := &Guard{Token: "secret", LoopbackFree: false, SessionTTL: time.Hour}
	srv := newAuthServer(t, g)

	page := doReq(t, "GET", srv.URL+"/login", "", nil)
	html, _ := io.ReadAll(page.Body)
	if page.StatusCode != 200 || !strings.Contains(string(html), `name="token"`) ||
		!strings.Contains(string(html), "/api/auth/token") {
		t.Fatalf("登录页应只有 token 输入框并提交到 /api/auth/token：%d %s", page.StatusCode, html)
	}
	if got := page.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options = %q，应为 DENY", got)
	}

	if resp := doReq(t, "POST", srv.URL+"/api/auth/token", `{"token":"wrong"}`,
		map[string]string{"Content-Type": "application/json", "Origin": srv.URL}); resp.StatusCode != 401 {
		t.Fatalf("错误 token 应 401，得到 %d", resp.StatusCode)
	}

	resp := doReq(t, "POST", srv.URL+"/api/auth/token", `{"token":"secret"}`,
		map[string]string{"Content-Type": "application/json", "Origin": srv.URL})
	if resp.StatusCode != 200 {
		t.Fatalf("正确 token 应 200，得到 %d", resp.StatusCode)
	}
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == CookieName {
			session = c
		}
	}
	if session == nil || session.Value == "" {
		t.Fatal("登录成功应 Set-Cookie reeve_session")
	}
	if !session.HttpOnly || session.SameSite != http.SameSiteStrictMode || session.MaxAge != 3600 {
		t.Fatalf("会话 Cookie 属性不对：%+v", session)
	}

	if resp := doReq(t, "GET", srv.URL+"/api/bots", "",
		map[string]string{"Cookie": CookieName + "=" + session.Value}); resp.StatusCode != 200 {
		t.Fatalf("带会话 Cookie 应 200，得到 %d", resp.StatusCode)
	}
	if resp := doReq(t, "GET", srv.URL+"/api/bots", "", nil); resp.StatusCode != 401 {
		t.Fatalf("没带凭证应 401，得到 %d", resp.StatusCode)
	}
}

func TestLogoutClearsSessionCookie(t *testing.T) {
	g := &Guard{Token: "secret", LoopbackFree: false, SessionTTL: time.Hour}
	srv := newAuthServer(t, g)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	call := func(method, path, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Origin", srv.URL)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}

	if resp := call("POST", "/api/auth/token", `{"token":"secret"}`); resp.StatusCode != 200 {
		t.Fatalf("登录应 200，得到 %d", resp.StatusCode)
	}
	if resp := call("GET", "/api/bots", ""); resp.StatusCode != 200 {
		t.Fatalf("带会话 Cookie 应 200，得到 %d", resp.StatusCode)
	}

	resp := call("POST", "/api/auth/logout", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("退出登录应 204，得到 %d", resp.StatusCode)
	}
	var cleared *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == CookieName {
			cleared = c
		}
	}
	if cleared == nil {
		t.Fatal("退出登录应清除 reeve_session，但没有 Set-Cookie")
	}
	if cleared.MaxAge >= 0 || cleared.Value != "" || cleared.Path != "/" ||
		!cleared.HttpOnly || cleared.SameSite != http.SameSiteStrictMode {
		t.Fatalf("清除 Cookie 的属性不对：%+v", cleared)
	}

	// 浏览器按 Max-Age=0 删掉 Cookie 后，再访问受保护接口就是未登录。
	if resp := call("GET", "/api/bots", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("退出登录后访问 /api/bots 应 401，得到 %d", resp.StatusCode)
	}
}

// 登录页引用 /favicon.svg，未登录时也必须能直接拿到；这里用不跟随重定向的 client，
// 否则 302 → /login → 200 会被跟随，断言就永远看不到重定向。
func TestFaviconIsPublic(t *testing.T) {
	g := &Guard{Token: "secret", LoopbackFree: false, SessionTTL: time.Hour}
	srv := newAuthServer(t, g)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/favicon.svg")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("未登录访问 /favicon.svg 应 200，得到 %d（Location=%q）", resp.StatusCode, resp.Header.Get("Location"))
	}
	if loc := resp.Header.Get("Location"); loc != "" {
		t.Fatalf("未登录访问 /favicon.svg 不应跳转，Location=%q", loc)
	}
}

func TestLoginAcceptsFormAndRejectsBadBody(t *testing.T) {
	g := &Guard{Token: "secret", LoopbackFree: false, SessionTTL: time.Hour}
	srv := newAuthServer(t, g)

	resp := doReq(t, "POST", srv.URL+"/api/auth/token", "token=secret",
		map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": srv.URL})
	if resp.StatusCode != 200 {
		t.Fatalf("表单登录应 200，得到 %d", resp.StatusCode)
	}
	if resp := doReq(t, "POST", srv.URL+"/api/auth/token", `{`,
		map[string]string{"Content-Type": "application/json", "Origin": srv.URL}); resp.StatusCode != 400 {
		t.Fatalf("坏 JSON 应 400，得到 %d", resp.StatusCode)
	}
	if resp := doReq(t, "POST", srv.URL+"/api/auth/token", `{"token":"secret"}`,
		map[string]string{"Content-Type": "text/plain", "Origin": srv.URL}); resp.StatusCode != 415 {
		t.Fatalf("不支持的 Content-Type 应 415，得到 %d", resp.StatusCode)
	}
}

func TestCrossSiteWritesRejected(t *testing.T) {
	g := &Guard{Token: "secret", LoopbackFree: true, SessionTTL: time.Hour}
	h := g.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	cases := []struct {
		name, method, host, origin, fetchSite string
		want                                  int
	}{
		{"同源写", "POST", "127.0.0.1:18800", "http://127.0.0.1:18800", "", 200},
		{"跨站 Origin", "POST", "127.0.0.1:18800", "https://evil.example", "", 403},
		{"Origin 端口不同", "POST", "127.0.0.1:18800", "http://127.0.0.1:9999", "", 403},
		{"无 Origin 但跨站", "POST", "127.0.0.1:18800", "", "cross-site", 403},
		{"GET 不拦", "GET", "127.0.0.1:18800", "https://evil.example", "", 200},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "http://"+c.host+"/api/bots", nil)
		req.Host, req.RemoteAddr = c.host, "127.0.0.1:5555"
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		if c.fetchSite != "" {
			req.Header.Set("Sec-Fetch-Site", c.fetchSite)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != c.want {
			t.Errorf("%s：%d，应为 %d", c.name, w.Code, c.want)
		}
		if got := w.Header().Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("%s：X-Frame-Options = %q", c.name, got)
		}
	}
}

func TestLoginRateLimit(t *testing.T) {
	g := &Guard{Token: "secret", LoopbackFree: false, SessionTTL: time.Hour}
	srv := newAuthServer(t, g)
	post := func(token, xff string) *http.Response {
		hdr := map[string]string{"Content-Type": "application/json", "Origin": srv.URL}
		if xff != "" {
			hdr["X-Forwarded-For"] = xff
		}
		return doReq(t, "POST", srv.URL+"/api/auth/token", `{"token":"`+token+`"}`, hdr)
	}
	for i := range loginMaxFails {
		if resp := post("wrong", ""); resp.StatusCode != 401 {
			t.Fatalf("第 %d 次错误应 401，得到 %d", i+1, resp.StatusCode)
		}
	}
	resp := post("wrong", "")
	if resp.StatusCode != 429 {
		t.Fatalf("5 分钟内失败 %d 次后应 429，得到 %d", loginMaxFails, resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("429 应带 Retry-After")
	}
	if resp := post("secret", ""); resp.StatusCode != 429 {
		t.Fatalf("被限速期间正确 token 也应 429，得到 %d", resp.StatusCode)
	}
	// 限速按来源 IP 计：换一个 IP 不应受影响（这里仍然只是 token 错，所以 401）。
	if resp := post("wrong", "198.51.100.7"); resp.StatusCode != 401 {
		t.Fatalf("其他 IP 不应被一起锁住，得到 %d", resp.StatusCode)
	}
}

// token 轮换必须对正在运行的服务立即生效：同一个 Guard、同一个服务器上，
// 旧 Cookie 与旧 Bearer 立刻 401，新 token 立刻可用。不能在轮换后重建 Guard，
// 否则测不出「启动快照」的问题。
func TestTokenRotationAppliesToRunningGuard(t *testing.T) {
	dir := t.TempDir()
	old, _, err := EnsureToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	g := &Guard{Token: old, TokenDir: dir, LoopbackFree: false, SessionTTL: time.Hour}
	srv := newAuthServer(t, g)
	login := func(token string, want int) string {
		t.Helper()
		resp := doReq(t, "POST", srv.URL+"/api/auth/token", `{"token":"`+token+`"}`,
			map[string]string{"Content-Type": "application/json", "Origin": srv.URL})
		if resp.StatusCode != want {
			t.Fatalf("登录 token=%q：%d，应为 %d", token, resp.StatusCode, want)
		}
		session := ""
		for _, c := range resp.Cookies() {
			if c.Name == CookieName {
				session = c.Name + "=" + c.Value
			}
		}
		return session
	}
	status := func(hdr map[string]string) int {
		t.Helper()
		return doReq(t, "GET", srv.URL+"/api/bots", "", hdr).StatusCode
	}
	session := login(old, 200)
	if session == "" {
		t.Fatal("登录应返回会话 Cookie")
	}
	if got := status(map[string]string{"Cookie": session}); got != 200 {
		t.Fatalf("轮换前 Cookie 应 200，得到 %d", got)
	}
	if got := status(map[string]string{"Authorization": "Bearer " + old}); got != 200 {
		t.Fatalf("轮换前 Bearer 应 200，得到 %d", got)
	}

	next, err := RotateToken(dir)
	if err != nil {
		t.Fatal(err)
	}

	if got := status(map[string]string{"Cookie": session}); got != 401 {
		t.Fatalf("轮换后同一 Guard 上的旧 Cookie 必须 401，得到 %d", got)
	}
	if got := status(map[string]string{"Authorization": "Bearer " + old}); got != 401 {
		t.Fatalf("轮换后同一 Guard 上的旧 Bearer 必须 401，得到 %d", got)
	}
	if got := status(map[string]string{"Authorization": "Bearer " + next}); got != 200 {
		t.Fatalf("轮换后的新 Bearer 应 200，得到 %d", got)
	}
	fresh := login(next, 200)
	if fresh == "" {
		t.Fatal("新 token 应能换到新会话")
	}
	if got := status(map[string]string{"Cookie": fresh}); got != 200 {
		t.Fatalf("新会话 Cookie 应 200，得到 %d", got)
	}
	login(old, 401)
}

// /api/auth/me 保持旧网页依赖的 enabled / authenticated 契约。
func TestAuthMeContract(t *testing.T) {
	g := &Guard{Token: "secret", LoopbackFree: false, SessionTTL: time.Hour}
	srv := newAuthServer(t, g)
	me := func(hdr map[string]string) map[string]any {
		t.Helper()
		resp := doReq(t, "GET", srv.URL+"/api/auth/me", "", hdr)
		if resp.StatusCode != 200 {
			t.Fatalf("GET /api/auth/me = %d", resp.StatusCode)
		}
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	anon := me(nil)
	if anon["enabled"] != true || anon["authenticated"] != false || anon["mode"] != "none" {
		t.Fatalf("未登录的 me 响应不对：%+v", anon)
	}
	withToken := me(map[string]string{"Authorization": "Bearer secret"})
	if withToken["enabled"] != true || withToken["authenticated"] != true || withToken["mode"] != "token" {
		t.Fatalf("带 Bearer 的 me 响应不对：%+v", withToken)
	}
	loop := &Guard{Token: "secret", LoopbackFree: true, SessionTTL: time.Hour}
	loopSrv := newAuthServer(t, loop)
	resp := doReq(t, "GET", loopSrv.URL+"/api/auth/me", "", nil)
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out["authenticated"] != true || out["mode"] != "loopback" {
		t.Fatalf("回环 me 响应不对：%+v", out)
	}
}

// 限速表达到上限时先清过期条目，仍然满就整表清空，避免被大量新鲜 IP 撑爆内存。
func TestLimiterTableBound(t *testing.T) {
	l := newLimiter()
	now := time.Now()
	for i := range maxLimiterEntries {
		l.ips[strconv.Itoa(i)] = []time.Time{now.Add(-time.Hour)}
	}
	l.fail("fresh", now)
	if _, ok := l.ips["fresh"]; !ok || len(l.ips) != 1 {
		t.Fatalf("过期条目应先被清掉，只留下新的 IP：%d 条", len(l.ips))
	}
	for i := range maxLimiterEntries {
		l.ips[strconv.Itoa(i)] = []time.Time{now}
	}
	l.fail("another", now)
	if len(l.ips) != 0 {
		t.Fatalf("全是新鲜 IP 时应清空整表，得到 %d 条", len(l.ips))
	}
}

func TestSecureCookieBehindTLSProxy(t *testing.T) {
	g := &Guard{Token: "secret", LoopbackFree: false, SessionTTL: time.Hour}
	srv := newAuthServer(t, g)
	resp := doReq(t, "POST", srv.URL+"/api/auth/token", `{"token":"secret"}`,
		map[string]string{"Content-Type": "application/json", "Origin": srv.URL, "X-Forwarded-Proto": "https"})
	for _, c := range resp.Cookies() {
		if c.Name == CookieName && !c.Secure {
			t.Fatal("经 HTTPS 代理时应给 Cookie 加 Secure")
		}
	}
}
