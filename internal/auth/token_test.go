package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestEnsureTokenPersistsAndIsStable(t *testing.T) {
	dir := t.TempDir()
	tok, created, err := EnsureToken(dir)
	if err != nil || !created || tok == "" {
		t.Fatalf("首次应生成 token：%v %v %q", err, created, tok)
	}
	again, created, err := EnsureToken(dir)
	if err != nil || created || again != tok {
		t.Fatalf("再次调用应返回同一个 token：%v %v", err, created)
	}
	st, err := os.Stat(filepath.Join(dir, tokenFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Fatalf("token 文件权限应为 0600，得到 %o", perm)
	}
}

func TestLoadTokenEmptyFileFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, tokenFile), []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToken(dir); err == nil {
		t.Fatal("空 token 文件应报错，而不是当成有效 token")
	}
	if _, _, err := EnsureToken(dir); err == nil {
		t.Fatal("空 token 文件不应被静默覆盖")
	}
}

func TestRotateInvalidatesOldSessions(t *testing.T) {
	dir := t.TempDir()
	old, _, _ := EnsureToken(dir)
	g := &Guard{Token: old, LoopbackFree: true, SessionTTL: time.Hour}
	cookie := g.issueCookie()
	if !g.validCookie(cookie.Value) {
		t.Fatal("刚签发的 Cookie 应该有效")
	}
	next, err := RotateToken(dir)
	if err != nil || next == old {
		t.Fatalf("轮换应产生新 token：%v", err)
	}
	if loaded, err := LoadToken(dir); err != nil || loaded != next {
		t.Fatalf("轮换后应能从文件读到新 token：%v", err)
	}
	g2 := &Guard{Token: next, LoopbackFree: true, SessionTTL: time.Hour}
	if g2.validCookie(cookie.Value) {
		t.Fatal("轮换后旧 Cookie 必须失效")
	}
}

func TestCookieExpires(t *testing.T) {
	g := &Guard{Token: "secret", SessionTTL: time.Hour}
	expired := strconv.FormatInt(time.Now().Add(-time.Second).Unix(), 10)
	if g.validCookie(expired + "." + signCookie(g.token(), expired)) {
		t.Fatal("过期的 Cookie 必须失效")
	}
	payload := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	if !g.validCookie(payload + "." + signCookie(g.token(), payload)) {
		t.Fatal("未过期的 Cookie 应有效")
	}
	// 篡改过期时间后签名失配。
	forged := strconv.FormatInt(time.Now().Add(48*time.Hour).Unix(), 10)
	if g.validCookie(forged + "." + signCookie(g.token(), payload)) {
		t.Fatal("篡改过的 Cookie 必须失效")
	}
}

func TestRemoteNeedsToken(t *testing.T) {
	g := &Guard{Token: "secret", LoopbackFree: true, SessionTTL: time.Hour}
	const remote = "203.0.113.9:5555"
	if got := statusOf(g, "reeve.example:18800", remote, nil); got != http.StatusUnauthorized {
		t.Fatalf("外部地址无 token 应 401，得到 %d", got)
	}
	if got := statusOf(g, "reeve.example:18800", remote, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer wrong")
	}); got != http.StatusUnauthorized {
		t.Fatalf("错误 token 应 401，得到 %d", got)
	}
	if got := statusOf(g, "reeve.example:18800", remote, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer secret")
	}); got != http.StatusOK {
		t.Fatalf("正确 Bearer 应 200，得到 %d", got)
	}
}

// DNS 重绑定：恶意域名解析到 127.0.0.1 时来源地址是本机，但 Host 不是，
// 不能因此获得回环免认证。
func TestRebindingBlockedByHostCheck(t *testing.T) {
	g := &Guard{Token: "secret", LoopbackFree: true, SessionTTL: time.Hour}
	if got := statusOf(g, "evil.example:18800", "127.0.0.1:5555", nil); got != http.StatusUnauthorized {
		t.Fatalf("非本机 Host + 回环来源必须 401，得到 %d", got)
	}
	if got := statusOf(g, "127.0.0.1:18800", "203.0.113.9:5555", nil); got != http.StatusUnauthorized {
		t.Fatalf("本机 Host + 外来源必须 401，得到 %d", got)
	}
	if got := statusOf(g, "evil.example:18800", "127.0.0.1:5555", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer secret")
	}); got != http.StatusOK {
		t.Fatalf("带正确 Bearer 时应放行，得到 %d", got)
	}
}

func TestLoopbackIsFree(t *testing.T) {
	g := &Guard{Token: "secret", LoopbackFree: true, SessionTTL: time.Hour}
	for _, host := range []string{"127.0.0.1:18800", "localhost:18800", "[::1]:18800"} {
		if got := statusOf(g, host, "127.0.0.1:5555", nil); got != http.StatusOK {
			t.Fatalf("回环 %s 应免认证，得到 %d", host, got)
		}
	}
	if got := statusOf(g, "127.0.0.1:18800", "[::1]:5555", nil); got != http.StatusOK {
		t.Fatalf("IPv6 回环来源应免认证，得到 %d", got)
	}
	g.LoopbackFree = false
	if got := statusOf(g, "127.0.0.1:18800", "127.0.0.1:5555", nil); got != http.StatusUnauthorized {
		t.Fatalf("关闭 LoopbackFree 后回环也应 401，得到 %d", got)
	}
}

func TestEmptyTokenFailsClosed(t *testing.T) {
	g := &Guard{LoopbackFree: false, SessionTTL: time.Hour}
	if got := statusOf(g, "reeve.example:18800", "203.0.113.9:5555", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer ")
	}); got != http.StatusUnauthorized {
		t.Fatalf("Token 为空时必须拒绝，得到 %d", got)
	}
}

func TestPublicPathsWithoutAuth(t *testing.T) {
	g := &Guard{Token: "secret", LoopbackFree: false, SessionTTL: time.Hour}
	for _, path := range []string{"/api/health", "/api/auth/me"} {
		if got := statusOfPath(g, path, "reeve.example:18800", "203.0.113.9:5555", nil); got != http.StatusOK {
			t.Fatalf("公开路径 %s 应 200，得到 %d", path, got)
		}
	}
}

func statusOf(g *Guard, host, remote string, mutate func(*http.Request)) int {
	return statusOfPath(g, "/api/bots", host, remote, mutate)
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
