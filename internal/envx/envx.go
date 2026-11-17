// Package envx 管理传给子进程的环境变量：哪些变量永远不能离开 reeve 进程。
//
// 内置执行器（shell 工具）与外部执行器（codex / pi / omp）都要用同一份 stdin/环境过滤，
// 否则一条新增的凭据只改了其中一边，另一边仍会泄漏。
package envx

import "strings"

// Credentials 是 reeve 自己的凭据：模型 Key、主密钥、访问密码。
// 任何子进程（内置 shell、外部 CLI）都不能继承。
var Credentials = map[string]bool{
	"API_KEY":          true,
	"CHAT_API_KEY":     true,
	"REEVE_API_KEY":    true,
	"REEVE_MASTER_KEY": true,
	"REEVE_PASSWORD":   true,
}

// VendorKeys 是会让 CLI 或 shell 命令改走厂商 API Key / 第三方地址的变量。
// 外部 CLI 在订阅模式下会把这几个额外剔除（见 cli.subscriptionEnv）。
var VendorKeys = []string{"XAI_API_KEY", "OPENAI_API_KEY"}

// Hidden 是默认不传给子进程的全集：reeve 自身凭据 + 厂商 Key。
var Hidden = func() map[string]bool {
	m := make(map[string]bool, len(Credentials)+len(VendorKeys))
	for k := range Credentials {
		m[k] = true
	}
	for _, k := range VendorKeys {
		m[k] = true
	}
	return m
}()

// Filter 返回去掉 drop 中键（大小写不敏感）的环境变量列表。
// drop 为 nil 时原样返回副本。
func Filter(environ []string, drop map[string]bool) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		key, _, _ := strings.Cut(kv, "=")
		if !drop[strings.ToUpper(key)] {
			out = append(out, kv)
		}
	}
	return out
}
