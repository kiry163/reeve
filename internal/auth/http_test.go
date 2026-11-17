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

func newTestGuard(t *testing.T) (*Guard, string) {
	t.Helper()
	fastHash(t)
	dir := t.TempDir()
	if err := SetPassword(dir, "secret-password"); err != nil {
		t.Fatal(err)
	}
	return &Guard{DataDir: dir, SessionTTL: time.Hour}, dir
}

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

func loginReq(t *testing.T, srv *httptest.Server, password string, want int) (string, *http.Cookie) {
	t.Helper()
	resp := doReq(t, "POST", srv.URL+"/api/auth/login", `{"password":"`+password+`"}`,
		map[string]string{"Content-Type": "application/json", "Origin": srv.URL})
	if resp.StatusCode != want {
		t.Fatalf("登录 password=%q：%d，应为 %d", password, resp.StatusCode, want)
	}
	if want != 200 {
		return "", nil
	}
	var out struct {
		OK    bool   `json:"ok"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || !out.OK || out.Token == "" {
		t.Fatalf("登录响应不对：%+v %v", out, err)
	}
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == CookieName {
			session = c
		}
	}
	if session == nil || session.Value == "" {
		t.Fatal("登录成功应返回会话 Cookie")
	}
	return out.Token, session
}

func TestLoginFlow(t *testing.T) {
	g, _ := newTestGuard(t)
	srv := newAuthServer(t, g)

	page := doReq(t, "GET", srv.URL+"/login", "", nil)
	html, _ := io.ReadAll(page.Body)
	if page.StatusCode != 200 || !strings.Contains(string(html), `name="password"`) ||
		!strings.Contains(string(html), "/api/auth/login") {
		t.Fatalf("登录页应只有密码输入框并提交到 /api/auth/login：%d %s", page.StatusCode, html)
	}
	if got := page.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options = %q，应为 DENY", got)
	}

	if resp := doReq(t, "POST", srv.URL+"/api/auth/login", `{"password":"wrong"}`,
		map[string]string{"Content-Type": "application/json", "Origin": srv.URL}); resp.StatusCode != 401 {
		t.Fatalf("错误密码应 401，得到 %d", resp.StatusCode)
	}

	token, session := loginReq(t, srv, "secret-password", 200)
	if !session.HttpOnly || session.SameSite != http.SameSiteStrictMode || session.MaxAge != 3600 {
		t.Fatalf("会话 Cookie 属性不对：%+v", session)
	}
	if resp := doReq(t, "GET", srv.URL+"/api/bots", "",
		map[string]string{"Cookie": CookieName + "=" + session.Value}); resp.StatusCode != 200 {
		t.Fatalf("带会话 Cookie 应 200，得到 %d", resp.StatusCode)
	}
	if resp := doReq(t, "GET", srv.URL+"/api/bots", "",
		map[string]string{"Authorization": "Bearer " + token}); resp.StatusCode != 200 {
		t.Fatalf("带 Bearer 会话令牌应 200，得到 %d", resp.StatusCode)
	}
	// 本机回环也不能免登录：httptest 的来源就是 127.0.0.1。
	if resp := doReq(t, "GET", srv.URL+"/api/bots", "", nil); resp.StatusCode != 401 {
		t.Fatalf("回环无凭证也应 401，得到 %d", resp.StatusCode)
	}
}

func TestLoginAcceptsFormAndRejectsBadBody(t *testing.T) {
	g, _ := newTestGuard(t)
	srv := newAuthServer(t, g)

	resp := doReq(t, "POST", srv.URL+"/api/auth/login", "password=secret-password",
		map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": srv.URL})
	if resp.StatusCode != 200 {
		t.Fatalf("表单登录应 200，得到 %d", resp.StatusCode)
	}
	if resp := doReq(t, "POST", srv.URL+"/api/auth/login", `{`,
		map[string]string{"Content-Type": "application/json", "Origin": srv.URL}); resp.StatusCode != 400 {
		t.Fatalf("坏 JSON 应 400，得到 %d", resp.StatusCode)
	}
	if resp := doReq(t, "POST", srv.URL+"/api/auth/login", `{"password":"secret-password"}`,
		map[string]string{"Content-Type": "text/plain", "Origin": srv.URL}); resp.StatusCode != 415 {
		t.Fatalf("不支持的 Content-Type 应 415，得到 %d", resp.StatusCode)
	}
	if resp := doReq(t, "POST", srv.URL+"/api/auth/login", `{"password":"   "}`,
		map[string]string{"Content-Type": "application/json", "Origin": srv.URL}); resp.StatusCode != 400 {
		t.Fatalf("空密码应 400，得到 %d", resp.StatusCode)
	}
}

func TestPasswordChangeInvalidatesOtherSessions(t *testing.T) {
	g, _ := newTestGuard(t)
	srv := newAuthServer(t, g)
	oldToken, oldCookie := loginReq(t, srv, "secret-password", 200)

	if resp := doReq(t, "POST", srv.URL+"/api/auth/password",
		`{"current_password":"wrong","new_password":"new-password"}`, map[string]string{
			"Content-Type": "application/json", "Origin": srv.URL,
			"Cookie": CookieName + "=" + oldCookie.Value,
		}); resp.StatusCode != 400 {
		t.Fatalf("当前密码错误应 400，得到 %d", resp.StatusCode)
	}

	resp := doReq(t, "POST", srv.URL+"/api/auth/password",
		`{"current_password":"secret-password","new_password":"new-password"}`, map[string]string{
			"Content-Type": "application/json", "Origin": srv.URL,
			"Cookie": CookieName + "=" + oldCookie.Value,
		})
	if resp.StatusCode != 200 {
		t.Fatalf("改密应 200，得到 %d", resp.StatusCode)
	}
	var fresh struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fresh); err != nil || fresh.Token == "" {
		t.Fatalf("改密应返回新会话令牌：%v", err)
	}
	var freshCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == CookieName {
			freshCookie = c
		}
	}
	if freshCookie == nil {
		t.Fatal("改密应刷新会话 Cookie")
	}

	// 旧 Cookie 与旧 Bearer 都失效；新会话可用。
	if got := doReq(t, "GET", srv.URL+"/api/bots", "",
		map[string]string{"Cookie": CookieName + "=" + oldCookie.Value}).StatusCode; got != 401 {
		t.Fatalf("改密后旧 Cookie 应 401，得到 %d", got)
	}
	if got := doReq(t, "GET", srv.URL+"/api/bots", "",
		map[string]string{"Authorization": "Bearer " + oldToken}).StatusCode; got != 401 {
		t.Fatalf("改密后旧 Bearer 应 401，得到 %d", got)
	}
	if got := doReq(t, "GET", srv.URL+"/api/bots", "",
		map[string]string{"Cookie": CookieName + "=" + freshCookie.Value}).StatusCode; got != 200 {
		t.Fatalf("新会话 Cookie 应 200，得到 %d", got)
	}
	if got := doReq(t, "GET", srv.URL+"/api/bots", "",
		map[string]string{"Authorization": "Bearer " + fresh.Token}).StatusCode; got != 200 {
		t.Fatalf("新 Bearer 应 200，得到 %d", got)
	}

	// 旧密码不能登录，新密码可以。
	loginReq(t, srv, "secret-password", 401)
	loginReq(t, srv, "new-password", 200)
}

func TestLogoutClearsSessionCookie(t *testing.T) {
	g, _ := newTestGuard(t)
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

	if resp := call("POST", "/api/auth/login", `{"password":"secret-password"}`); resp.StatusCode != 200 {
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

	if resp := call("GET", "/api/bots", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("退出登录后访问 /api/bots 应 401，得到 %d", resp.StatusCode)
	}
}

// 登录页引用 /favicon.svg，未登录时也必须能直接拿到；这里用不跟随重定向的 client，
// 否则 302 → /login → 200 会被跟随，断言就永远看不到重定向。
func TestFaviconIsPublic(t *testing.T) {
	g, _ := newTestGuard(t)
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

func TestCrossSiteWritesRejected(t *testing.T) {
	g, _ := newTestGuard(t)
	token, _, err := g.newSession()
	if err != nil {
		t.Fatal(err)
	}
	h := g.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	cases := []struct {
		name, method, host, origin, fetchSite string
		want                                  int
	}{
		{"同源写", "POST", "127.0.0.1:18800", "http://127.0.0.1:18800", "", 204},
		{"跨站 Origin", "POST", "127.0.0.1:18800", "https://evil.example", "", 403},
		{"Origin 端口不同", "POST", "127.0.0.1:18800", "http://127.0.0.1:9999", "", 403},
		{"无 Origin 但跨站", "POST", "127.0.0.1:18800", "", "cross-site", 403},
		{"GET 不拦", "GET", "127.0.0.1:18800", "https://evil.example", "", 204},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "http://"+c.host+"/api/bots", nil)
		req.Host, req.RemoteAddr = c.host, "127.0.0.1:5555"
		req.Header.Set("Authorization", "Bearer "+token)
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
	g, _ := newTestGuard(t)
	srv := newAuthServer(t, g)
	post := func(password, xff string) *http.Response {
		hdr := map[string]string{"Content-Type": "application/json", "Origin": srv.URL}
		if xff != "" {
			hdr["X-Forwarded-For"] = xff
		}
		return doReq(t, "POST", srv.URL+"/api/auth/login", `{"password":"`+password+`"}`, hdr)
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
	if resp := post("secret-password", ""); resp.StatusCode != 429 {
		t.Fatalf("被限速期间正确密码也应 429，得到 %d", resp.StatusCode)
	}
	// 限速按来源 IP 计：换一个 IP 不应受影响（这里仍然只是密码错，所以 401）。
	if resp := post("wrong", "198.51.100.7"); resp.StatusCode != 401 {
		t.Fatalf("其他 IP 不应被一起锁住，得到 %d", resp.StatusCode)
	}
}

// /api/auth/me 保持网页与 Android 依赖的 enabled / authenticated / mode 契约。
func TestAuthMeContract(t *testing.T) {
	g, _ := newTestGuard(t)
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
	token, session := loginReq(t, srv, "secret-password", 200)
	withToken := me(map[string]string{"Authorization": "Bearer " + token})
	if withToken["enabled"] != true || withToken["authenticated"] != true || withToken["mode"] != "bearer" {
		t.Fatalf("带 Bearer 的 me 响应不对：%+v", withToken)
	}
	withCookie := me(map[string]string{"Cookie": CookieName + "=" + session.Value})
	if withCookie["enabled"] != true || withCookie["authenticated"] != true || withCookie["mode"] != "session" {
		t.Fatalf("带 Cookie 的 me 响应不对：%+v", withCookie)
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
	g, _ := newTestGuard(t)
	srv := newAuthServer(t, g)
	resp := doReq(t, "POST", srv.URL+"/api/auth/login", `{"password":"secret-password"}`,
		map[string]string{"Content-Type": "application/json", "Origin": srv.URL, "X-Forwarded-Proto": "https"})
	for _, c := range resp.Cookies() {
		if c.Name == CookieName && !c.Secure {
			t.Fatal("经 HTTPS 代理时应给 Cookie 加 Secure")
		}
	}
}

func TestPublicPathsWithoutAuth(t *testing.T) {
	g, _ := newTestGuard(t)
	for _, path := range []string{"/api/health", "/api/auth/me"} {
		if got := statusOfPath(g, path, "reeve.example:18800", "203.0.113.9:5555", nil); got != http.StatusOK {
			t.Fatalf("公开路径 %s 应 200，得到 %d", path, got)
		}
	}
	if got := statusOfPath(g, "/api/bots", "127.0.0.1:18800", "127.0.0.1:5555", nil); got != http.StatusUnauthorized {
		t.Fatalf("回环未登录访问 /api/bots 应 401，得到 %d", got)
	}
}

func statusOfPath(g *Guard, path, host, remote string, mutate func(*http.Request)) int {
	h := g.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	req := httptest.NewRequest(http.MethodGet, "http://"+host+path, nil)
	req.Host, req.RemoteAddr = host, remote
	if mutate != nil {
		mutate(req)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code
}
