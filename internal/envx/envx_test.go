package envx

import (
	"strings"
	"testing"
)

// 凭据与厂商 Key 大小写不敏感地被剔除，其他变量原样保留。
func TestFilterDropsCredentialsCaseInsensitive(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"REEVE_MASTER_KEY=secret",
		"reeve_api_key=secret",
		"API_KEY=secret",
		"CHAT_API_KEY=secret",
		"REEVE_PASSWORD=secret",
		"XAI_API_KEY=secret",
		"openai_api_key=secret",
		"KEEP=1",
	}
	out := strings.Join(Filter(env, Hidden), "\n")
	for _, leaked := range []string{"REEVE_MASTER_KEY", "reeve_api_key", "API_KEY=secret", "CHAT_API_KEY",
		"REEVE_PASSWORD", "XAI_API_KEY", "openai_api_key"} {
		if strings.Contains(strings.ToUpper(out), strings.ToUpper(leaked)) {
			t.Fatalf("%s 泄漏：%s", leaked, out)
		}
	}
	if !strings.Contains(out, "PATH=/usr/bin") || !strings.Contains(out, "KEEP=1") {
		t.Fatalf("普通变量应保留：%s", out)
	}
	if Filter(nil, Hidden) == nil || len(Filter(nil, Hidden)) != 0 {
		t.Fatal("空环境应返回空切片")
	}
}

// Hidden 是 Credentials 与 VendorKeys 的并集：两边不能再各维护一份。
func TestHiddenIsSingleSource(t *testing.T) {
	for k := range Credentials {
		if !Hidden[k] {
			t.Fatalf("%s 不在 Hidden 里", k)
		}
	}
	for _, k := range VendorKeys {
		if !Hidden[k] {
			t.Fatalf("%s 不在 Hidden 里", k)
		}
	}
}
