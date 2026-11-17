package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kiry163/reeve/internal/atomicfile"
)

const (
	passwordFile     = "password"
	sessionKeyFile   = "session.key"
	saltLen          = 16
	hashLen          = 32
	minPasswordLen   = 8
	maxPasswordLen   = 256
	passwordAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"
)

// passwordIterations 是 PBKDF2 的迭代次数；测试会临时调低，避免每次登录都跑满。
var passwordIterations = 600_000

// EnsurePassword 确保数据目录里有密码哈希和会话签名密钥。
// 首次调用会生成一个随机初始密码并返回明文（只返回这一次）；之后返回空串。
func EnsurePassword(dir string) (initial string, created bool, err error) {
	path := filepath.Join(dir, passwordFile)
	if _, statErr := os.Stat(path); statErr == nil {
		if _, err := readPasswordHash(path); err != nil {
			return "", false, err
		}
		return "", false, ensureSessionKey(dir)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", false, statErr
	}
	pw, err := randomPassword()
	if err != nil {
		return "", false, err
	}
	if err := SetPassword(dir, pw); err != nil {
		return "", false, err
	}
	return pw, true, nil
}

// SetPassword 保存新密码的哈希，并轮换会话密钥（所有已登录设备立即失效）。
func SetPassword(dir, password string) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	hash := pbkdf2SHA256([]byte(password), salt, passwordIterations, hashLen)
	line := fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", passwordIterations,
		base64.RawURLEncoding.EncodeToString(salt), base64.RawURLEncoding.EncodeToString(hash))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := atomicfile.Write(filepath.Join(dir, passwordFile), []byte(line+"\n"), 0o600); err != nil {
		return err
	}
	return rotateSessionKey(dir)
}

// VerifyPassword 校验密码。密码文件不存在或损坏时返回错误，调用方应拒绝登录。
func VerifyPassword(dir, password string) (bool, error) {
	h, err := readPasswordHash(filepath.Join(dir, passwordFile))
	if err != nil {
		return false, err
	}
	got := pbkdf2SHA256([]byte(password), h.salt, h.iterations, len(h.hash))
	return subtle.ConstantTimeCompare(got, h.hash) == 1, nil
}

// ValidatePassword 是密码长度的唯一校验入口（网页、CLI 与自动生成共用）。
func ValidatePassword(password string) error {
	if len(password) < minPasswordLen {
		return fmt.Errorf("密码至少 %d 个字符", minPasswordLen)
	}
	if len(password) > maxPasswordLen {
		return fmt.Errorf("密码最多 %d 个字符", maxPasswordLen)
	}
	if strings.TrimSpace(password) == "" {
		return errors.New("密码不能全是空白")
	}
	if strings.ContainsAny(password, "\r\n") {
		return errors.New("密码不能包含换行")
	}
	return nil
}

type passwordHash struct {
	iterations int
	salt       []byte
	hash       []byte
}

func readPasswordHash(path string) (passwordHash, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return passwordHash{}, err
	}
	return parsePasswordHash(strings.TrimSpace(string(raw)))
}

func parsePasswordHash(raw string) (passwordHash, error) {
	parts := strings.Split(raw, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return passwordHash{}, errors.New("密码文件格式不对：请运行 reeve passwd 重设密码")
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1000 || iter > 5_000_000 {
		return passwordHash{}, errors.New("密码文件的迭代次数不合法：请运行 reeve passwd 重设密码")
	}
	salt, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(salt) < 8 {
		return passwordHash{}, errors.New("密码文件的盐不合法：请运行 reeve passwd 重设密码")
	}
	hash, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(hash) < 16 {
		return passwordHash{}, errors.New("密码文件的哈希不合法：请运行 reeve passwd 重设密码")
	}
	return passwordHash{iterations: iter, salt: salt, hash: hash}, nil
}

func randomPassword() (string, error) {
	b := make([]byte, 20)
	n := big.NewInt(int64(len(passwordAlphabet)))
	for i := range b {
		idx, err := rand.Int(rand.Reader, n)
		if err != nil {
			return "", err
		}
		b[i] = passwordAlphabet[idx.Int64()]
	}
	return string(b), nil
}

// pbkdf2SHA256 是 RFC 8018 的 PBKDF2-HMAC-SHA256。标准库没有内置实现，
// 这里只用 hmac/sha256，避免为一个本地登录功能引入额外依赖。
func pbkdf2SHA256(password, salt []byte, iterations, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	blocks := (keyLen + hashLen - 1) / hashLen
	dk := make([]byte, 0, blocks*hashLen)
	u := make([]byte, hashLen)
	t := make([]byte, hashLen)
	var counter [4]byte
	for block := 1; block <= blocks; block++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(counter[:], uint32(block))
		prf.Write(counter[:])
		u = prf.Sum(u[:0])
		copy(t, u)
		for n := 2; n <= iterations; n++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(u[:0])
			for i := range t {
				t[i] ^= u[i]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}
