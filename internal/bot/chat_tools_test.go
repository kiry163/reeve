package bot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiry163/reeve/internal/builtin"
)

// 聊天层可以在工作区内直接读写文件（不弹审批），但不能执行命令、不能改环境变量。
func TestChatLayerWorkspaceTools(t *testing.T) {
	b := newBot(t, t.TempDir(), nil)
	defer b.Close()
	got := map[string]bool{}
	for _, spec := range b.registry().Specs() {
		got[spec.Name] = true
	}
	for _, banned := range []string{"append_file", "shell", "exec", "set_env", "unset_env"} {
		if got[banned] {
			t.Fatalf("聊天层不应暴露 %s", banned)
		}
	}
	for _, need := range []string{"start_task", "read_file", "write_file", "edit_file", "list_dir", "search_files", "list_env", "ask_user"} {
		if !got[need] {
			t.Fatalf("聊天层应保留 %s", need)
		}
	}
}

// 工作区内的写文件、改文件由聊天层直接执行：不弹审批卡，也不派后台任务。
func TestChatWorkspaceWritesNeedNoApproval(t *testing.T) {
	dir := t.TempDir()
	b := newBot(t, dir, &fakeAI{agent: blockingAgent})
	b.Start()
	defer b.Close()
	if _, err := b.Send("直接写文件"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "written", func() bool { return countMsgs(b.store, "assistant", "开始做了") == 1 })
	if _, err := b.Send("直接改文件"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "edited", func() bool { return countMsgs(b.store, "assistant", "开始做了") == 2 })
	if data, err := os.ReadFile(filepath.Join(dir, "ws", "notes", "a.txt")); err != nil || string(data) != "hi" {
		t.Fatalf("文件内容应为 hi，得到 %q (%v)", data, err)
	}
	if n := len(b.store.PendingApprovals()); n != 0 {
		t.Fatalf("工作区读写不应产生审批，得到 %d 张", n)
	}
	if n := len(b.store.Tasks()); n != 0 {
		t.Fatalf("直接读写不应派任务，得到 %d 个", n)
	}
}

// 免审批不等于放开路径：写出工作区仍然被拒绝，且不会弹卡。
func TestChatWorkspaceWriteCannotEscape(t *testing.T) {
	dir := t.TempDir()
	b := newBot(t, dir, &fakeAI{agent: blockingAgent})
	b.Start()
	defer b.Close()
	if _, err := b.Send("写越界文件"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "reply", func() bool { return countMsgs(b.store, "assistant", "开始做了") == 1 })
	if _, err := os.Stat(filepath.Join(dir, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("越界写入不应落地: %v", err)
	}
	if n := len(b.store.PendingApprovals()); n != 0 {
		t.Fatalf("越界写入不应弹审批，得到 %d 张", n)
	}
}

// chatTool 按名字取聊天层工具，取不到直接失败。
func chatTool(t *testing.T, b *Bot, name string) builtin.Tool {
	t.Helper()
	if tl := b.registry().Get(name); tl != nil {
		return tl
	}
	t.Fatalf("聊天层没有 %s", name)
	return nil
}

// list_env 只读环境变量，永远不显示 vault 里的密钥值。
func TestChatListEnvHidesSecrets(t *testing.T) {
	b := newBot(t, t.TempDir(), nil)
	defer b.Close()
	if err := b.vault.Set("API_TOKEN", "s3cret-value", "测试用", nil); err != nil {
		t.Fatal(err)
	}
	if err := b.SetInstanceEnv("INSTANCE_VAR", "iv", "test"); err != nil {
		t.Fatal(err)
	}
	out, err := chatTool(t, b, "list_env").Execute(context.Background(), builtin.ToolEnv{}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "INSTANCE_VAR=iv") {
		t.Errorf("list_env 应输出实例级变量：\n%s", out)
	}
	if strings.Contains(out, "bot 级") {
		t.Errorf("list_env 不应再输出 bot 级：\n%s", out)
	}
	if !strings.HasSuffix(out, "密钥不在环境变量里，只能用 {{secret:NAME}} 引用。") {
		t.Errorf("末尾应固定提示密钥按名字引用：\n%s", out)
	}
	// 反向断言：既不含密钥值，也不含密钥名与 [secret: 标记。聊天层 wrapper 会把
	// vault 值替换成 [secret:API_TOKEN]，所以只要实现真的读过 vault，这里必红。
	assertNoSecret(t, "list_env 输出", out)

	// 更硬的证明：绕开聊天层 wrapper，直接看数据源。EnvReport 的输入来自 b.InstanceEnv()，
	// 其中本来就不该有密钥，因此不用脱敏也找不到密钥值或密钥名。
	instance, err := b.InstanceEnv()
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, "EnvReport 数据源", builtin.EnvReport(instance))
}

// assertNoSecret 断言文本里没有测试密钥的值、名字或脱敏标记。
func assertNoSecret(t *testing.T, what, text string) {
	t.Helper()
	for _, leak := range []string{"s3cret-value", "API_TOKEN", "[secret:"} {
		if strings.Contains(text, leak) {
			t.Fatalf("%s 出现了密钥痕迹 %q：\n%s", what, leak, text)
		}
	}
}

// View 失败时错误信息要能让模型向用户解释；用例用坏行弄坏实例级 env 文件。
func TestChatListEnvViewError(t *testing.T) {
	dir := t.TempDir()
	b := newBot(t, dir, nil)
	defer b.Close()
	if err := os.WriteFile(filepath.Join(dir, "data", "env"), []byte("BROKEN LINE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := chatTool(t, b, "list_env").Execute(context.Background(), builtin.ToolEnv{}, json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "读不到环境变量") {
		t.Fatalf("应返回读取错误，得到 %v", err)
	}
}
