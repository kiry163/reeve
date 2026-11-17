package auth

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fastHash(t *testing.T) {
	t.Helper()
	old := passwordIterations
	passwordIterations = 1000
	t.Cleanup(func() { passwordIterations = old })
}

func TestEnsurePasswordGeneratesOnceAndPersistsHash(t *testing.T) {
	fastHash(t)
	dir := t.TempDir()
	initial, created, err := EnsurePassword(dir)
	if err != nil || !created || len(initial) != 20 {
		t.Fatalf("首次应生成 20 位初始密码：err=%v created=%v password=%q", err, created, initial)
	}
	if ok, err := VerifyPassword(dir, initial); err != nil || !ok {
		t.Fatalf("初始密码应能通过校验：%v %v", ok, err)
	}
	if ok, _ := VerifyPassword(dir, "wrong-password"); ok {
		t.Fatal("错误密码不应通过校验")
	}
	pwFile := filepath.Join(dir, passwordFile)
	raw, err := os.ReadFile(pwFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), initial) {
		t.Fatal("密码文件不能保存明文密码")
	}
	if !strings.HasPrefix(string(raw), "pbkdf2-sha256$") {
		t.Fatalf("密码文件格式不对：%q", raw)
	}
	if st, err := os.Stat(pwFile); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("密码文件权限应为 0600：%v", err)
	}
	key, err := LoadSessionKey(dir)
	if err != nil || len(key) != 32 {
		t.Fatalf("应生成 32 字节会话密钥：%v %d", err, len(key))
	}
	if st, err := os.Stat(filepath.Join(dir, sessionKeyFile)); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("会话密钥权限应为 0600：%v", err)
	}

	again, created, err := EnsurePassword(dir)
	if err != nil || created || again != "" {
		t.Fatalf("已有密码时不应再生成：err=%v created=%v password=%q", err, created, again)
	}
}

func TestEnsurePasswordRejectsCorruptFile(t *testing.T) {
	fastHash(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, passwordFile), []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := EnsurePassword(dir); err == nil {
		t.Fatal("损坏的密码文件必须报错，不能静默覆盖")
	}
}

func TestSetPasswordRotatesSessions(t *testing.T) {
	fastHash(t)
	dir := t.TempDir()
	if err := SetPassword(dir, "first-password"); err != nil {
		t.Fatal(err)
	}
	g := &Guard{DataDir: dir, SessionTTL: time.Hour}
	oldToken, _, err := g.newSession()
	if err != nil {
		t.Fatal(err)
	}
	if !checkSessionToken(g.key(), oldToken) {
		t.Fatal("刚签发的会话应有效")
	}
	if err := SetPassword(dir, "second-password"); err != nil {
		t.Fatal(err)
	}
	if checkSessionToken(g.key(), oldToken) {
		t.Fatal("改密后旧会话必须失效")
	}
	if ok, _ := VerifyPassword(dir, "first-password"); ok {
		t.Fatal("旧密码不应再通过校验")
	}
	if ok, err := VerifyPassword(dir, "second-password"); err != nil || !ok {
		t.Fatalf("新密码应通过校验：%v %v", ok, err)
	}
	fresh, _, err := g.newSession()
	if err != nil || !checkSessionToken(g.key(), fresh) {
		t.Fatalf("改密后的新会话应有效：%v", err)
	}
}

func TestValidatePassword(t *testing.T) {
	for _, bad := range []string{"", "short", "        ", "line\nbreak"} {
		if err := ValidatePassword(bad); err == nil {
			t.Fatalf("密码 %q 应被拒绝", bad)
		}
	}
	if err := ValidatePassword("good-password"); err != nil {
		t.Fatalf("合法密码不应报错：%v", err)
	}
	if err := ValidatePassword(strings.Repeat("a", maxPasswordLen+1)); err == nil {
		t.Fatal("超长密码应被拒绝")
	}
}

// RFC 8018 PBKDF2-HMAC-SHA256 的公开测试向量，确保自实现没有跑偏。
func TestPBKDF2SHA256KnownVector(t *testing.T) {
	got := hex.EncodeToString(pbkdf2SHA256([]byte("password"), []byte("salt"), 1, 32))
	const want = "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"
	if got != want {
		t.Fatalf("PBKDF2 结果不对：%s", got)
	}
}
