package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kiry163/reeve/internal/atomicfile"
)

const (
	// CookieName 是网页会话 Cookie 的名字。
	CookieName        = "reeve_session"
	defaultSessionTTL = 30 * 24 * time.Hour
)

// Guard 校验每个请求：公开路径直接放行；其余请求必须带登录换来的
// 会话令牌（Authorization: Bearer 或 reeve_session Cookie）。
// 没有回环例外：本机访问同样必须登录。
type Guard struct {
	// DataDir 是 password 与 session.key 所在目录。
	DataDir string
	// SessionTTL 是会话有效期，默认 30 天。
	SessionTTL time.Duration
}

func (g *Guard) ttl() time.Duration {
	if g.SessionTTL > 0 {
		return g.SessionTTL
	}
	return defaultSessionTTL
}

func (g *Guard) key() []byte {
	if g == nil || g.DataDir == "" {
		return nil
	}
	key, err := LoadSessionKey(g.DataDir)
	if err != nil {
		return nil
	}
	return key
}

// authorized 判断请求是否已认证；没有有效会话一律拒绝（fail closed）。
func (g *Guard) authorized(r *http.Request) bool {
	key := g.key()
	return len(key) > 0 && (bearerOK(key, r) || cookieOK(key, r))
}

func bearerOK(key []byte, r *http.Request) bool {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return false
	}
	return checkSessionToken(key, strings.TrimPrefix(h, "Bearer "))
}

func cookieOK(key []byte, r *http.Request) bool {
	c, err := r.Cookie(CookieName)
	return err == nil && checkSessionToken(key, c.Value)
}

// mode 说明当前请求是凭什么身份通过认证的，供 /api/auth/me 展示。
func (g *Guard) mode(r *http.Request) string {
	key := g.key()
	switch {
	case len(key) == 0:
		return "none"
	case bearerOK(key, r):
		return "bearer"
	case cookieOK(key, r):
		return "session"
	default:
		return "none"
	}
}

// ---------- 会话令牌 ----------
// 令牌 = "<过期 Unix 秒>.<HMAC-SHA256(session.key, 前缀+过期时间)>"。
// 网页把它放进 HttpOnly Cookie，Android 把它作为 Bearer；两者校验同一份签名。

const sessionSignPrefix = "reeve-session|"

func signSession(key []byte, payload string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(sessionSignPrefix + payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func newSessionToken(key []byte, ttl time.Duration) (string, time.Time) {
	expires := time.Now().Add(ttl)
	payload := strconv.FormatInt(expires.Unix(), 10)
	return payload + "." + signSession(key, payload), expires
}

func checkSessionToken(key []byte, value string) bool {
	if len(key) == 0 {
		return false
	}
	payload, sig, ok := strings.Cut(value, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(signSession(key, payload))) {
		return false
	}
	exp, err := strconv.ParseInt(payload, 10, 64)
	return err == nil && time.Now().Unix() < exp
}

// newSession 用当前的 session.key 签发会话令牌。
func (g *Guard) newSession() (string, time.Time, error) {
	key, err := LoadSessionKey(g.DataDir)
	if err != nil {
		return "", time.Time{}, err
	}
	token, expires := newSessionToken(key, g.ttl())
	return token, expires, nil
}

func newCookie(token string, ttl time.Duration) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(ttl / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
}

func secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// ---------- session.key ----------

// LoadSessionKey 读取会话签名密钥。文件不存在或长度不对时返回错误，调用方一律拒绝。
func LoadSessionKey(dir string) ([]byte, error) {
	key, err := os.ReadFile(filepath.Join(dir, sessionKeyFile))
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, os.ErrInvalid
	}
	return key, nil
}

// ensureSessionKey 在缺失或损坏时生成新密钥；已有有效密钥时保持不变。
func ensureSessionKey(dir string) error {
	if key, err := LoadSessionKey(dir); err == nil && len(key) == 32 {
		return nil
	}
	return rotateSessionKey(dir)
}

// rotateSessionKey 生成新的会话密钥，使所有旧会话立即失效。
func rotateSessionKey(dir string) error {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dir, sessionKeyFile), key, 0o600)
}
