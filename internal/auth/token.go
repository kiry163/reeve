// Package auth 是访问控制的唯一入口：一个 access token（data/token，0600）、
// 用 token 做 HMAC 签名的会话 Cookie（reeve_session），以及回环地址免认证。
// 回环免认证必须同时满足 Host 与来源地址都是本机，否则恶意网页把域名解析到
// 127.0.0.1 时也能通过（DNS 重绑定）。跨站写请求在中间件里统一拦截。
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// CookieName 是网页会话 Cookie 的名字。
	CookieName = "reeve_session"

	tokenFile         = "token"
	defaultSessionTTL = 7 * 24 * time.Hour
)

// EnsureToken 读取数据目录里的 token；不存在时生成 32 字节随机数（base64url）
// 并写入 0600 的 data/token。created 表示这次新建了 token。
func EnsureToken(dir string) (token string, created bool, err error) {
	tok, err := LoadToken(dir)
	if err == nil {
		return tok, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", false, err
	}
	tok = base64.RawURLEncoding.EncodeToString(buf)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(filepath.Join(dir, tokenFile), []byte(tok+"\n"), 0o600); err != nil {
		return "", false, err
	}
	return tok, true, nil
}

// LoadToken 读取数据目录里的 token。文件不存在时返回的错误满足 errors.Is(err, os.ErrNotExist)。
func LoadToken(dir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, tokenFile))
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		return "", errors.New("token 文件是空的：请运行 reeve token rotate 重新生成")
	}
	return tok, nil
}

// RotateToken 删除旧 token 并生成新的。会话 Cookie 的签名密钥就是 token，
// 所以轮换后所有已签发的 Cookie 立即失效。运行中的服务不需要重启：
// Guard 配置了 TokenDir 时每次鉴权都重新读取 token 文件。
func RotateToken(dir string) (string, error) {
	if err := os.Remove(filepath.Join(dir, tokenFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	tok, _, err := EnsureToken(dir)
	return tok, err
}

// Guard 校验每个请求：公开路径直接放行；回环地址（Host 与来源地址都是本机）
// 在 LoopbackFree 打开时免认证；其他请求必须带 Authorization: Bearer <token>
// 或有效的会话 Cookie。Token 为空时一律拒绝访问（fail closed）。
type Guard struct {
	// Token 是兜底 token，只在 TokenDir 为空时使用。
	Token string
	// TokenDir 非空时，每次鉴权都重新读取 <TokenDir>/token。这样运行中执行
	// token rotate 后，旧的 Bearer 与旧的会话 Cookie 立即失效，不需要重启。
	TokenDir     string
	LoopbackFree bool
	SessionTTL   time.Duration
}

func (g *Guard) ttl() time.Duration {
	if g.SessionTTL > 0 {
		return g.SessionTTL
	}
	return defaultSessionTTL
}

// token 返回本次鉴权应当使用的 token。配置了 TokenDir 就每次读文件（44 字节，
// 回环请求的开销可以忽略）；读不到时返回空串，调用方一律拒绝（fail closed）。
func (g *Guard) token() string {
	if g.TokenDir == "" {
		return g.Token
	}
	tok, err := LoadToken(g.TokenDir)
	if err != nil {
		return ""
	}
	return tok
}

// authorized 判断请求是否已认证。同一次判断只读一次 token，避免比较时用到两个版本。
func (g *Guard) authorized(r *http.Request) bool {
	tok := g.token()
	return g.loopback(r) || bearerOK(tok, r) || cookieOK(tok, r)
}

// loopback 是回环免认证：Host 和来源地址都必须指向本机。
// 只看 RemoteAddr 挡不住 DNS 重绑定——恶意域名解析到 127.0.0.1 后来源也是本机，
// 但浏览器发出的 Host 头仍然是恶意域名。
func (g *Guard) loopback(r *http.Request) bool {
	return g.LoopbackFree && isLoopbackHost(hostname(r.Host)) && isLoopbackHost(hostname(r.RemoteAddr))
}

// bearerOK 用给定的当前 token 校验 Bearer；token 为空时一律拒绝。
func bearerOK(token string, r *http.Request) bool {
	if token == "" {
		return false
	}
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(h, "Bearer ")), []byte(token)) == 1
}

func cookieOK(token string, r *http.Request) bool {
	c, err := r.Cookie(CookieName)
	return err == nil && checkCookie(token, c.Value)
}

// mode 说明当前请求是凭什么身份通过认证的，供 /api/auth/me 展示。
func (g *Guard) mode(r *http.Request) string {
	tok := g.token()
	switch {
	case g.loopback(r):
		return "loopback"
	case bearerOK(tok, r):
		return "token"
	case cookieOK(tok, r):
		return "session"
	default:
		return "none"
	}
}

// ---------- 会话 Cookie ----------
// Cookie 值 = "<过期 Unix 秒>.<HMAC-SHA256(token, 前缀+过期时间)>"。
// 签名密钥就是 token，轮换 token 等于让所有旧 Cookie 失效；过期时间也在签名里，不能改。

const cookieSignPrefix = "reeve-session|"

func signCookie(token, payload string) string {
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte(cookieSignPrefix + payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// newCookie 用给定的当前 token 签发会话 Cookie。
func newCookie(token string, ttl time.Duration) *http.Cookie {
	payload := strconv.FormatInt(time.Now().Add(ttl).Unix(), 10)
	return &http.Cookie{
		Name:     CookieName,
		Value:    payload + "." + signCookie(token, payload),
		Path:     "/",
		MaxAge:   int(ttl / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
}

func checkCookie(token, value string) bool {
	if token == "" {
		return false
	}
	payload, sig, ok := strings.Cut(value, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(signCookie(token, payload))) {
		return false
	}
	exp, err := strconv.ParseInt(payload, 10, 64)
	return err == nil && time.Now().Unix() < exp
}

// issueCookie 按当前的 token 签一张 Cookie（测试与内部使用）。
func (g *Guard) issueCookie() *http.Cookie { return newCookie(g.token(), g.ttl()) }

// validCookie 按当前的 token 校验 Cookie（测试与内部使用）。
func (g *Guard) validCookie(value string) bool { return checkCookie(g.token(), value) }

func secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// ---------- 主机名 / 回环地址 ----------

// isLoopbackHost 判断一个主机名（不带端口）是不是本机：localhost、*.localhost
// 或回环 IP（127.0.0.0/8、::1）。
func isLoopbackHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

// hostname 从 host:port 里取出主机名；没有端口时原样返回（去掉 IPv6 的方括号）。
func hostname(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return strings.Trim(hostport, "[]")
}
