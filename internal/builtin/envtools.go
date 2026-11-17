package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/kiry163/reeve/internal/envstore"
)

// EnvStore 是环境变量的读写接口，由 bot 层注入（*Bot 实现了它）。
// 内置工具只读写 bot 级变量，实例级只做展示。
type EnvStore interface {
	View() (instance, bot map[string]string, err error)
	SetBotEnv(name, value, source string) error
	UnsetBotEnv(name, source string) error
}

// envSource 是内置工具写环境变量时记录的审计来源（区别于用户直接操作）。
const envSource = "builtin"

var listEnvTool = FuncTool{"list_env", "列出实例级与 bot 级环境变量（KEY=VALUE）。密钥不在环境变量里，不要写进 env。",
	`{"type":"object","properties":{}}`,
	readOnly, func(ctx context.Context, env ToolEnv, raw json.RawMessage) (string, error) {
		return listEnv(env)
	}}

// setEnvTool 自己处理密钥引用：Guard 不替换值里的 {{secret:…}}，由 setEnv 拒绝，
// 避免 vault 明文被写进 env.json。
var setEnvTool = PassSecrets{FuncTool{"set_env", "设置一个 bot 级环境变量，值会以环境变量形式传给后续 shell 命令，下一个任务生效。密钥请让用户在聊天里保存，不要写进 env（env 的值会出现在模型上下文与界面里）。",
	`{"type":"object","properties":{"name":{"type":"string","description":"变量名：字母或下划线开头，只含字母、数字、下划线"},"value":{"type":"string","description":"变量值：不能含换行、回车或 NUL；显式空串表示清空该变量"}},"required":["name"]}`,
	writes, func(ctx context.Context, env ToolEnv, raw json.RawMessage) (string, error) {
		return setEnv(env, raw)
	}}}

var unsetEnvTool = FuncTool{"unset_env", "删除一个 bot 级环境变量。实例级变量不在这里删。",
	`{"type":"object","properties":{"name":{"type":"string","description":"变量名（例如 HTTPS_PROXY）"}},"required":["name"]}`,
	writes, func(ctx context.Context, env ToolEnv, raw json.RawMessage) (string, error) {
		return unsetEnv(env, raw)
	}}

func listEnv(env ToolEnv) (string, error) {
	if env.EnvStore == nil {
		return "", errors.New("当前执行环境不支持环境变量")
	}
	instance, bot, err := env.EnvStore.View()
	if err != nil {
		return "", fmt.Errorf("读取环境变量失败: %w", err)
	}
	return EnvReport(instance, bot), nil
}

// EnvReport 按统一格式输出实例级与 bot 级环境变量（KEY=VALUE）。
// 内置 list_env 与聊天层的只读 list_env 共用；输出只有变量本身，
// 密钥值永远不在环境变量里，末尾固定提示按名字引用。
func EnvReport(instance, bot map[string]string) string {
	var b strings.Builder
	b.WriteString("实例级环境变量（所有 Bot 共享）：\n")
	writeEnvSection(&b, instance)
	b.WriteString("\nbot 级环境变量（只对这个 Bot 生效）：\n")
	writeEnvSection(&b, bot)
	b.WriteString("\n密钥不在环境变量里，只能用 {{secret:NAME}} 引用。")
	return b.String()
}

// writeEnvSection 按变量名排序输出 KEY=VALUE；空层明确写「（无）」。
func writeEnvSection(b *strings.Builder, vars map[string]string) {
	if len(vars) == 0 {
		b.WriteString("（无）\n")
		return
	}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "%s=%s\n", k, vars[k])
	}
}

func setEnv(env ToolEnv, raw json.RawMessage) (string, error) {
	if env.EnvStore == nil {
		return "", errors.New("当前执行环境不支持修改环境变量")
	}
	a, err := parseArgs[struct {
		Name  string
		Value *string
	}](raw)
	if err != nil {
		return "", err
	}
	if err := envstore.ValidName(a.Name); err != nil {
		return "", err
	}
	if a.Value == nil {
		return "", fmt.Errorf("需要 value；要删除变量请用 unset_env")
	}
	if secretRef.MatchString(*a.Value) || strings.Contains(*a.Value, "{{secret:") {
		return "", errors.New("密钥永不进入 env 存储，请不要在 set_env 的值里引用 {{secret:NAME}}；请在 shell 等工具参数里直接用 {{secret:NAME}}")
	}
	if err := envstore.ValidValue(*a.Value); err != nil {
		return "", fmt.Errorf("环境变量 %s 的值不合法: %w", a.Name, err)
	}
	if err := env.EnvStore.SetBotEnv(a.Name, *a.Value, envSource); err != nil {
		return "", err
	}
	if *a.Value == "" {
		return fmt.Sprintf("已清空 %s（bot 级空值，实例级同名变量也不再生效）；要显式删除请用 unset_env", a.Name), nil
	}
	out := fmt.Sprintf("已设置 %s（bot 级，下一个任务生效）", a.Name)
	if LooksLikeSecret(*a.Value) {
		out += "\n这个值看起来像密钥；密钥请让用户在聊天里保存，env 的值会出现在模型上下文与界面里。"
	}
	return out, nil
}

func unsetEnv(env ToolEnv, raw json.RawMessage) (string, error) {
	if env.EnvStore == nil {
		return "", errors.New("当前执行环境不支持修改环境变量")
	}
	a, err := parseArgs[struct{ Name string }](raw)
	if err != nil {
		return "", err
	}
	if err := envstore.ValidName(a.Name); err != nil {
		return "", err
	}
	instance, bot, err := env.EnvStore.View()
	if err != nil {
		// View 会先读实例级文件，实例级有坏行不应阻断 bot 级删除；
		// UnsetBotEnv 只碰 env.json，对不存在的键幂等。
		if uerr := env.EnvStore.UnsetBotEnv(a.Name, envSource); uerr != nil {
			return "", uerr
		}
		return fmt.Sprintf("已删除 %s", a.Name), nil
	}
	if _, ok := bot[a.Name]; !ok {
		if _, shared := instance[a.Name]; shared {
			return fmt.Sprintf("本来就没有 %s（它在实例级设置，unset_env 只删 bot 级；请到设置页删除）", a.Name), nil
		}
		return fmt.Sprintf("本来就没有 %s", a.Name), nil
	}
	if err := env.EnvStore.UnsetBotEnv(a.Name, envSource); err != nil {
		return "", err
	}
	return fmt.Sprintf("已删除 %s", a.Name), nil
}

// secretPrefixes 是常见密钥的固定前缀。
var secretPrefixes = []string{"sk-", "ghp_", "xoxb-", "AKIA"}

// LooksLikeSecret 粗判一个值是否像密钥：命中常见前缀，或长度 ≥32 的高熵
// base64/hex 串。只用于提示，不参与校验；内置工具的 set_env 与 HTTP 的
// PUT /api/env 共用这一判定。
func LooksLikeSecret(v string) bool {
	s := strings.TrimSpace(v)
	for _, p := range secretPrefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return len(s) >= 32 && isEncodedAlphabet(s) && shannonEntropy(s) >= 3.0
}

// isEncodedAlphabet 判断是否只由 base64 / base64url / hex 会用到的字符组成。
func isEncodedAlphabet(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '+', r == '/', r == '=', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// shannonEntropy 返回每字符信息熵（比特）。随机 hex 约 3.7–4，随机 base64 约 4.5–5。
func shannonEntropy(s string) float64 {
	if s == "" {
		return 0
	}
	counts := map[rune]int{}
	for _, r := range s {
		counts[r]++
	}
	n := float64(len(s))
	var e float64
	for _, c := range counts {
		p := float64(c) / n
		e -= p * math.Log2(p)
	}
	return e
}
