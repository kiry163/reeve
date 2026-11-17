package bot

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiry163/reeve/internal/builtin"
)

func TestDefaultPolicyIsUntrusted(t *testing.T) {
	dir := t.TempDir()
	b, err := New(Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws")})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.cfg.Policy != builtin.PolicyUntrusted {
		t.Fatalf("任务审批策略默认值应为 untrusted，得到 %q", b.cfg.Policy)
	}
	if b.cfg.ChatPolicy != builtin.PolicyUntrusted {
		t.Fatalf("聊天层审批策略默认值应为 untrusted，得到 %q", b.cfg.ChatPolicy)
	}
}

func TestNewRejectsUnsupportedPolicy(t *testing.T) {
	legacy := builtin.Policy("auto") // 已移除的旧策略（审核模型）
	_, err := New(Config{DataDir: t.TempDir(), Workspace: filepath.Join(t.TempDir(), "ws"), Policy: legacy})
	if err == nil {
		t.Fatal("任务审批策略 auto 应该被拒绝")
	}
	if !strings.Contains(err.Error(), "untrusted / on-request / never") {
		t.Fatalf("错误信息应说明可选策略，得到 %q", err)
	}
	_, err = New(Config{DataDir: t.TempDir(), Workspace: filepath.Join(t.TempDir(), "ws"), ChatPolicy: legacy})
	if err == nil {
		t.Fatal("聊天层审批策略 auto 应该被拒绝")
	}
	if !strings.Contains(err.Error(), "untrusted / on-request / never") {
		t.Fatalf("错误信息应说明可选策略，得到 %q", err)
	}
}
