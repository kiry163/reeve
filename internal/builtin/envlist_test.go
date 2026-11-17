package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EnvList 非空时就是 shell 的完整环境。
func TestShellUsesEnvList(t *testing.T) {
	dir := t.TempDir()
	out, err := ShellTool.Execute(context.Background(), ToolEnv{Workspace: dir, EnvList: []string{"PATH=/usr/bin:/bin", "REEVE_X=from-envlist"}}, json.RawMessage(`{"command":"printf %s \"$REEVE_X\""}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "from-envlist") {
		t.Fatalf("shell 未使用 EnvList：%q", out)
	}
}

// EnvList 为 nil 时保持既有行为：继承 SafeEnviron 的进程环境。
func TestShellNilEnvListInheritsProcessEnv(t *testing.T) {
	t.Setenv("REEVE_TEST_INHERIT", "inherited")
	dir := t.TempDir()
	out, err := ShellTool.Execute(context.Background(), ToolEnv{Workspace: dir}, json.RawMessage(`{"command":"printf %s \"$REEVE_TEST_INHERIT\""}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "inherited") {
		t.Fatalf("EnvList 为 nil 时 shell 未继承进程环境：%q", out)
	}
}

// EnvList 非 nil 时是完整替换：不在其中的进程环境变量不能泄漏给命令。
func TestShellEnvListReplacesProcessEnv(t *testing.T) {
	t.Setenv("REEVE_TEST_ONLY_PROCESS", "x")
	dir := t.TempDir()
	out, err := ShellTool.Execute(context.Background(), ToolEnv{Workspace: dir, EnvList: []string{"PATH=/usr/bin:/bin"}}, json.RawMessage(`{"command":"printf 'v=%s.' \"$REEVE_TEST_ONLY_PROCESS\""}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "v=.") {
		t.Fatalf("EnvList 应完整替换进程环境，但命令读到了 REEVE_TEST_ONLY_PROCESS：%q", out)
	}
}

// ToolEnv.Env（密钥注入）追加在 EnvList 之后，同名键以它为准。
func TestShellEnvOverridesEnvList(t *testing.T) {
	dir := t.TempDir()
	env := ToolEnv{Workspace: dir, EnvList: []string{"PATH=/usr/bin:/bin", "REEVE_X=from-envlist"}, Env: map[string]string{"REEVE_X": "from-secret"}}
	out, err := ShellTool.Execute(context.Background(), env, json.RawMessage(`{"command":"printf %s \"$REEVE_X\""}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "from-secret") || strings.Contains(out, "from-envlist") {
		t.Fatalf("Env 未覆盖 EnvList 的同名键：%q", out)
	}
}

// shell 的 env 参数是调用级层：覆盖已有变量、新增变量，PATH 前置到现有 PATH 之前。
func TestShellCallEnvOverlay(t *testing.T) {
	dir := t.TempDir()
	env := ToolEnv{Workspace: dir, EnvList: []string{"PATH=/usr/bin:/bin", "REEVE_X=from-envlist"}}
	args := json.RawMessage(`{"command":"printf '%s;%s;%s' \"$REEVE_X\" \"$REEVE_Y\" \"$PATH\"","env":{"REEVE_X":"from-call","REEVE_Y":"new","PATH":"/tmp/tools"}}`)
	out, err := ShellTool.Execute(context.Background(), env, args)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := "/tmp/tools" + string(filepath.ListSeparator) + "/usr/bin:/bin"
	if want := "from-call;new;" + wantPath + "\n[exit code 0]"; out != want {
		t.Fatalf("调用级 env 未生效：%q，期望 %q", out, want)
	}
}

// 调用级 env 不改变 ToolEnv.EnvList 本身：后续命令仍是原环境。
func TestShellCallEnvDoesNotPersist(t *testing.T) {
	dir := t.TempDir()
	env := ToolEnv{Workspace: dir, EnvList: []string{"PATH=/usr/bin:/bin"}}
	if _, err := ShellTool.Execute(context.Background(), env, json.RawMessage(`{"command":"true","env":{"REEVE_ONLY_ONCE":"1"}}`)); err != nil {
		t.Fatal(err)
	}
	out, err := ShellTool.Execute(context.Background(), env, json.RawMessage(`{"command":"printf 'v=%s.' \"$REEVE_ONLY_ONCE\""}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "v=.") {
		t.Fatalf("调用级 env 不应泄漏到下一次命令：%q", out)
	}
}

// 调用级 env 校验失败时回工具错误，命令不执行。
func TestShellCallEnvInvalidIsRejected(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	for _, raw := range []string{
		`{"command":"touch ran","env":{"1BAD":"x"}}`,
		`{"command":"touch ran","env":{"OK":"{{secret:PG_PASS}}"}}`,
		`{"command":"touch ran","env":{"OK":"a\nb"}}`,
	} {
		out, err := ShellTool.Execute(context.Background(), ToolEnv{Workspace: dir, EnvList: []string{"PATH=/usr/bin:/bin"}}, json.RawMessage(raw))
		if err == nil {
			t.Fatalf("%s 应被拒绝，得到输出 %q", raw, out)
		}
		if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
			t.Fatalf("%s 被拒绝时不应执行命令", raw)
		}
	}
}

// 不传 env 时命令行环境与 EnvList 完全一致（引入调用级之前的行为）。
func TestShellNoCallEnvUnchanged(t *testing.T) {
	dir := t.TempDir()
	env := ToolEnv{Workspace: dir, EnvList: []string{"PATH=/usr/bin:/bin", "REEVE_X=from-envlist"}}
	out, err := ShellTool.Execute(context.Background(), env, json.RawMessage(`{"command":"env | sort"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "REEVE_X=from-envlist") || !strings.Contains(out, "PATH=/usr/bin:/bin") || strings.Contains(out, "REEVE_ONLY_ONCE") {
		t.Fatalf("未传 env 时环境不应变化：%q", out)
	}
}
