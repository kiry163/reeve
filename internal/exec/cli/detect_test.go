package cli

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/kiry163/reeve/internal/exec"
)

func TestValidateOptions(t *testing.T) {
	got, err := ValidateOptions(exec.KindOMP, Options{Model: " gpt-5 "})
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "gpt-5" || got.Auth != AuthSubscription {
		t.Fatalf("默认认证方式应为 subscription：%#v", got)
	}

	got, err = ValidateOptions(exec.KindPi, Options{Auth: AuthEnv, Model: "m", Channel: "ch"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Auth != AuthChannel || got.Channel != "ch" {
		t.Fatalf("Pi 必须走 reeve 的 API 渠道：%#v", got)
	}

	if _, err := ValidateOptions(exec.KindPi, Options{Auth: AuthChannel, Channel: "bad id!"}); err == nil {
		t.Fatal("渠道 ID 含空格应报错")
	}
	if _, err := ValidateOptions(exec.KindCodex, Options{Model: "-oops"}); err == nil {
		t.Fatal("模型名不能以符号开头")
	}
	if _, err := ValidateOptions(exec.KindCodex, Options{Auth: "nope"}); err == nil {
		t.Fatal("无效认证方式应报错")
	}
	if _, err := ValidateOptions("unknown", Options{}); err == nil {
		t.Fatal("未知执行器应报错")
	}
}

func TestCommandEnvFiltersKeys(t *testing.T) {
	processEnv := []string{
		"PATH=/usr/bin",
		"OPENAI_API_KEY=sk-process",
		"REEVE_API_KEY=secret",
		"ANTHROPIC_API_KEY=sk-anthropic",
	}
	base := []string{"PATH=/usr/bin", "OPENAI_API_KEY=sk-task", "REEVE_API_KEY=secret"}

	sub := CommandEnv(processEnv, base, exec.KindCodex, AuthSubscription)
	if containsEnv(sub, "OPENAI_API_KEY") || containsEnv(sub, "REEVE_API_KEY") {
		t.Fatalf("订阅模式不应传厂商 Key：%v", sub)
	}
	if !containsEnv(sub, "PATH") {
		t.Fatalf("PATH 应保留：%v", sub)
	}

	env := CommandEnv(processEnv, base, exec.KindCodex, AuthEnv)
	if !containsEnv(env, "OPENAI_API_KEY=sk-task") {
		t.Fatalf("auth=env 应保留任务环境里的厂商 Key：%v", env)
	}
	pi := CommandEnv(processEnv, []string{"PATH=/usr/bin"}, exec.KindPi, AuthEnv)
	if !containsEnv(pi, "ANTHROPIC_API_KEY=sk-anthropic") {
		t.Fatalf("auth=env 应从进程环境补齐缺失的厂商 Key：%v", pi)
	}

	inherited := CommandEnv(processEnv, nil, exec.KindCodex, AuthSubscription)
	if containsEnv(inherited, "REEVE_API_KEY") || containsEnv(inherited, "OPENAI_API_KEY") {
		t.Fatalf("继承进程环境时也必须去掉 reeve 密钥与厂商 Key：%v", inherited)
	}
}

func TestWithPathDirPrependsOnce(t *testing.T) {
	env := withPathDir([]string{"PATH=/bin"}, "/opt/cli/bin")
	if !containsEnv(env, "PATH=/opt/cli/bin:/bin") {
		t.Fatalf("PATH 不对：%v", env)
	}
	if again := withPathDir(env, "/opt/cli/bin"); !slices.Equal(again, env) {
		t.Fatalf("已在 PATH 里时不应重复插入：%v", again)
	}
}

func TestAvailableParsesVersions(t *testing.T) {
	cases := []struct{ out, want string }{
		{"codex-cli 0.169.0", "0.169.0"},
		{"omp v18.11.0\n", "18.11.0"},
		{"omp/18.11.0", "18.11.0"},
		{"0.12.3", "0.12.3"},
		{"pi 0.12.3-beta.1\n", "0.12.3-beta.1"},
		{"", ""},
		{"usage: pi [options]", ""},
	}
	for _, c := range cases {
		if got := parseVersion(c.out); got != c.want {
			t.Fatalf("parseVersion(%q) = %q，应为 %q", c.out, got, c.want)
		}
	}
}

func TestDetectAndRegistry(t *testing.T) {
	status := Detect(context.Background())
	if len(status) != 3 {
		t.Fatalf("应返回三个执行器：%#v", status)
	}
	for _, st := range status {
		if st.ID == "" || st.Name == "" {
			t.Fatalf("缺少 ID/Name：%#v", st)
		}
	}
	r := NewRegistry()
	want := []exec.Kind{exec.KindCodex, exec.KindOMP, exec.KindPi}
	if got := r.Kinds(); !slices.Equal(got, want) {
		t.Fatalf("注册的执行器不符：%v", got)
	}
	if Name(exec.KindOMP) != "omp" || Name("") != "" {
		t.Fatalf("Name 不符：%q", Name(exec.KindOMP))
	}
}

func containsEnv(env []string, key string) bool {
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if k == key || kv == key {
			return true
		}
	}
	return false
}

// CommandEnv(auth=env) 特意从进程环境补回厂商 Key；Launch 层的过滤只剥 reeve 自身凭据，
// 不能再把 OPENAI_API_KEY 删掉（否则 codex 的 api-key 模式会静默认证失败）。
func TestAuthEnvVendorKeySurvivesLaunchFilter(t *testing.T) {
	processEnv := []string{"PATH=/usr/bin", "OPENAI_API_KEY=sk-process", "REEVE_MASTER_KEY=secret"}
	env := CommandEnv(processEnv, []string{"PATH=/usr/bin"}, exec.KindCodex, AuthEnv)
	env = launchEnv(env)
	if !containsEnv(env, "OPENAI_API_KEY=sk-process") {
		t.Fatalf("auth=env 补进来的厂商 Key 被 Launch 剥掉了：%v", env)
	}
	if containsEnv(env, "REEVE_MASTER_KEY") {
		t.Fatalf("reeve 主密钥不应进子进程：%v", env)
	}
}
