package bot

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"

	"github.com/kiry163/reeve/internal/vault"
)

// redactor 是唯一的脱敏入口，合并三类敏感值：
//   - 主密钥：启动时提供的形式（REEVE_MASTER_KEY 或 data/master.key）以及它的 hex 形式；
//   - 密钥库里的所有密钥（含 base64 / URL 编码等变体），由 vault 负责；
//   - 本机模型网关签发过的临时令牌。
//
// 事件写盘、SSE 推送、聊天层输入/输出、工具输出和检查点都要经过它。
type redactor struct {
	vault   *vault.Vault
	master  []string       // 主密钥需要抹掉的字面形式（都足够长，误伤风险低）
	gateway *gatewayTokens // 与所有 Bot 共享：令牌是实例级的，任何一个 Bot 的外部任务都可能把它带进输出
}

// gatewayTokens 是本机模型网关签发过的临时令牌集合。网关是设备上唯一的一个，令牌对所有
// Bot 的外部任务都有效，所以各 Bot 的脱敏器共享同一个集合：主 Bot 签发的令牌，其它 Bot
// 的 stream.jsonl / SSE 里出现时也必须抹掉。
type gatewayTokens struct {
	mu   sync.RWMutex
	toks []string
}

func (s *gatewayTokens) add(tok string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.toks = append(s.toks, tok)
	s.mu.Unlock()
}

func (s *gatewayTokens) snapshot() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.toks...)
}

const (
	masterMask  = "[主密钥已隐藏]"
	gatewayMask = "[网关令牌已隐藏]"
)

// addGatewayToken 登记一个本机网关令牌：签发即生效，之后事件写盘、SSE、工具
// 输出与错误文案里出现它都会被抹掉。
func (r *redactor) addGatewayToken(tok string) {
	if r == nil || len(tok) < 16 {
		return
	}
	r.gateway.add(tok)
}

// newRedactor 组装脱敏器。masterRaw 是 REEVE_MASTER_KEY 的原文（可为空）；
// 无论它是什么形式，真正用来加解密的 32 字节都会以 hex 形式一并脱敏。
func newRedactor(v *vault.Vault, key []byte, masterRaw string) *redactor {
	r := &redactor{vault: v, gateway: &gatewayTokens{}}
	if len(key) == 32 {
		r.master = append(r.master, hex.EncodeToString(key))
	}
	if raw := strings.TrimSpace(masterRaw); len(raw) >= 8 && raw != hex.EncodeToString(key) {
		r.master = append(r.master, raw)
		r.master = append(r.master, vault.Variants(raw)...)
	}
	return r
}

// Redact 返回抹掉所有敏感值之后的文本。
func (r *redactor) Redact(s string) string {
	if s == "" || r == nil {
		return s
	}
	if r.vault != nil {
		s = r.vault.Redact(s)
	}
	for _, m := range r.master {
		s = strings.ReplaceAll(s, m, masterMask)
	}
	for _, tok := range r.gateway.snapshot() {
		s = strings.ReplaceAll(s, tok, gatewayMask)
	}
	return s
}

// RedactBytes 是 Redact 的字节版本（写盘前的 JSON、检查点）。
func (r *redactor) RedactBytes(b []byte) []byte {
	if r == nil || len(b) == 0 {
		return b
	}
	return []byte(r.Redact(string(b)))
}

// guardedSecrets 把 vault 暴露给审批管线的同时，用统一脱敏器处理工具输出
// （shell / grep / read_file 等的结果在返回给模型前都会经过这里）。
type guardedSecrets struct {
	vault *vault.Vault
	red   *redactor
}

func (g guardedSecrets) Allowed(tool string, args json.RawMessage, names []string) error {
	return g.vault.Allowed(tool, args, names)
}

func (g guardedSecrets) Values(names []string) (map[string]string, error) {
	return g.vault.Values(names)
}

func (g guardedSecrets) Redact(s string) string { return g.red.Redact(s) }

// redactText 是聊天层各处统一使用的字符串脱敏。
func (b *Bot) redactText(s string) string { return b.redactor.Redact(s) }
