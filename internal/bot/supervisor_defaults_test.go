package bot

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kiry163/reeve/internal/builtin"
)

// TestNewAppliesRuntimeDefaults 走真实装配，防止 newRuntime 漏调 applyDefaults：
// 只测 applyDefaults 本身的话，调用被删掉时数值断言仍会绿。
func TestNewAppliesRuntimeDefaults(t *testing.T) {
	dir := t.TempDir()
	b, err := New(Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws")})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.cfg.MaxConcurrent != 3 || b.cfg.StallTimeout != 90*time.Second || b.cfg.ExternalStallTimeout != 10*time.Minute || b.cfg.Name != "reeve" {
		t.Fatalf("默认值未生效：%+v", b.cfg)
	}
}

func TestSlotDefaultIsThree(t *testing.T) {
	s := newTaskSlots(0)
	if s.max != 3 {
		t.Fatalf("并发上限默认应为 3，得到 %d", s.max)
	}
}

func TestDefaultTimeouts(t *testing.T) {
	cfg := Config{}
	applyDefaults(&cfg)
	if cfg.MaxConcurrent != 3 {
		t.Fatalf("MaxConcurrent 默认 3，得到 %d", cfg.MaxConcurrent)
	}
	if cfg.TaskMaxTime != 30*time.Minute {
		t.Fatalf("TaskMaxTime 默认 30m，得到 %v", cfg.TaskMaxTime)
	}
	if cfg.StallTimeout != 90*time.Second {
		t.Fatalf("StallTimeout 默认 90s，得到 %v", cfg.StallTimeout)
	}
	if cfg.ExternalStallTimeout != 10*time.Minute {
		t.Fatalf("ExternalStallTimeout 默认 10m，得到 %v", cfg.ExternalStallTimeout)
	}
}

func TestDefaultNameAndPolicies(t *testing.T) {
	cfg := Config{}
	applyDefaults(&cfg)
	if cfg.Name != "reeve" {
		t.Fatalf("名称默认 reeve，得到 %q", cfg.Name)
	}
	if cfg.Policy != builtin.PolicyUntrusted {
		t.Fatalf("审批策略默认 untrusted，得到 %q", cfg.Policy)
	}
	if cfg.ChatPolicy != builtin.PolicyUntrusted {
		t.Fatalf("聊天层审批策略默认 untrusted，得到 %q", cfg.ChatPolicy)
	}
	if cfg.TurnTimeout != 90*time.Second {
		t.Fatalf("聊天层单轮超时保持 90s，得到 %v", cfg.TurnTimeout)
	}
	if cfg.ContextTokens != 60000 {
		t.Fatalf("ContextTokens 默认 60000，得到 %d", cfg.ContextTokens)
	}
}

func TestApplyDefaultsKeepsExplicitValues(t *testing.T) {
	cfg := Config{
		Name:                 "自定义",
		Policy:               builtin.PolicyNever,
		ChatPolicy:           builtin.PolicyOnRequest,
		MaxConcurrent:        7,
		TaskMaxTime:          time.Minute,
		StallTimeout:         10 * time.Second,
		ExternalStallTimeout: 20 * time.Second,
		TurnTimeout:          5 * time.Second,
		ContextTokens:        1234,
	}
	applyDefaults(&cfg)
	if cfg.Name != "自定义" || cfg.Policy != builtin.PolicyNever || cfg.ChatPolicy != builtin.PolicyOnRequest ||
		cfg.MaxConcurrent != 7 || cfg.TaskMaxTime != time.Minute || cfg.StallTimeout != 10*time.Second ||
		cfg.ExternalStallTimeout != 20*time.Second ||
		cfg.TurnTimeout != 5*time.Second || cfg.ContextTokens != 1234 {
		t.Fatalf("显式填写的值不应被默认值覆盖：%+v", cfg)
	}
}
