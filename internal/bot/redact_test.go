package bot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiry163/reeve/internal/builtin"
)

func setTestSecret(t *testing.T, b *Bot, value string) {
	t.Helper()
	if err := b.vault.Set("API_TOKEN", value, "测试用", nil); err != nil {
		t.Fatal(err)
	}
}

// 用户把配对串粘进聊天：持久事件与 History 里都不能有明文。
func TestPairStringNeverPersisted(t *testing.T) {
	dir := t.TempDir()
	b := newBot(t, dir, &fakeAI{agent: blockingAgent})
	defer b.Close()

	pair := "reeve://pair?v=1&host=192.168.1.20:18800"
	m, err := b.Send("这是我的配对串 " + pair)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.Text, pair) {
		t.Fatalf("returned message leaks pair string: %q", m.Text)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "data", "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), pair) {
		t.Fatal("stream.jsonl contains the pair string")
	}
	if n := countMsgs(b.store, "notice", "疑似密钥"); n == 0 {
		t.Error("pasting the pair string should prompt the chat layer to use a secret card")
	}
}

// 密钥库里的值在聊天、事件、检查点与工具输出里都只出现占位符。
func TestVaultSecretNeverPersisted(t *testing.T) {
	dir := t.TempDir()
	b := newBot(t, dir, &fakeAI{agent: blockingAgent})
	defer b.Close()
	const secret = "s3cret-value-123456"
	setTestSecret(t, b, secret)

	m, err := b.Send("这是我的密钥：" + secret)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.Text, secret) {
		t.Fatalf("returned message leaks secret: %q", m.Text)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "data", "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("stream.jsonl contains the secret")
	}
	for _, msg := range b.store.History(20) {
		if strings.Contains(msg.Text, secret) {
			t.Fatalf("History leaks secret: %q", msg.Text)
		}
	}
}

func TestShellOutputRedactedBeforeModel(t *testing.T) {
	dir := t.TempDir()
	const secret = "s3cret-value-shell"
	ai := &fakeAI{agent: func(_ context.Context, ms []builtin.Message) (builtin.Message, error) {
		if toolSteps(ms) == 0 {
			return toolCall("shell", map[string]string{"command": "printf '%s\\n' '" + secret + "'"}), nil
		}
		return builtin.Message{Content: "读完了"}, nil
	}}
	b := newBot(t, dir, ai)
	defer b.Close()
	setTestSecret(t, b, secret)
	b.Start()
	id, _ := b.StartTask("打印环境", "打印")
	waitFor(t, "task done", func() bool { rec, _ := b.store.Task(id); return rec.Status == "completed" })
	found := false
	for _, req := range ai.reqs {
		var body struct{ Messages []builtin.Message }
		if json.Unmarshal([]byte(req), &body) != nil {
			continue
		}
		for _, m := range body.Messages {
			if m.Role == "tool" {
				found = true
				if strings.Contains(m.Content, secret) {
					t.Fatalf("tool result fed to the model leaks secret: %q", m.Content)
				}
			}
		}
	}
	if !found {
		t.Fatal("expected the shell tool result to reach the model")
	}
}

// 检查点写入与载入都要脱敏。
func TestCheckpointRedacted(t *testing.T) {
	dir := t.TempDir()
	b := newBot(t, dir, &fakeAI{agent: blockingAgent})
	defer b.Close()
	const secret = "s3cret-value-checkpoint"
	setTestSecret(t, b, secret)
	b.saveCheckpoint("task_x", map[string]any{"messages": []builtin.Message{{Role: "user", Content: "key=" + secret}}, "steps": 1})
	raw, err := os.ReadFile(b.checkpointPath("task_x"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("checkpoint file contains the secret")
	}
	cp, ok := b.loadCheckpoint("task_x")
	if !ok || strings.Contains(cp.Messages[0].Content, secret) {
		t.Fatalf("loaded checkpoint leaks secret: %+v", cp)
	}
}

// 主密钥（data/master.key 的 hex）也不能出现在任何输出里。
func TestMasterKeyRedacted(t *testing.T) {
	dir := t.TempDir()
	b := newBot(t, dir, &fakeAI{agent: blockingAgent})
	defer b.Close()
	raw, err := os.ReadFile(filepath.Join(dir, "data", "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	key := strings.TrimSpace(string(raw))
	if len(key) < 16 {
		t.Fatalf("unexpected master key file: %q", key)
	}
	if got := b.redactText("master=" + key); got != "master="+masterMask {
		t.Fatalf("master key not redacted: %q", got)
	}
}

// 聊天层的 list_tasks / read_task / search_history 输出在返回前统一过脱敏器。
func TestChatReadToolsRedacted(t *testing.T) {
	dir := t.TempDir()
	b := newBot(t, dir, &fakeAI{agent: blockingAgent})
	defer b.Close()
	const secret = "s3cret-value-summary"
	setTestSecret(t, b, secret)
	b.store.Append("task.created", TaskRec{ID: "task_leak", Title: "泄漏", Input: "无害", Status: "paused", CreatedAt: time.Now()}, false)
	b.store.mu.Lock()
	if tsk := b.store.tasks["task_leak"]; tsk != nil {
		tsk.Summary = "token " + secret
	}
	b.store.mu.Unlock()
	for _, name := range []string{"list_tasks", "read_task", "search_history"} {
		tool := b.registry().Get(name)
		if tool == nil {
			t.Fatalf("missing tool %s", name)
		}
		args := map[string]any{"task_id": "task_leak", "query": "token"}
		if name == "list_tasks" {
			args = map[string]any{"status": "all"}
		}
		raw, _ := json.Marshal(args)
		out, err := tool.Execute(context.Background(), builtin.ToolEnv{Workspace: b.cfg.Workspace}, raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(out, secret) {
			t.Fatalf("%s leaks secret: %q", name, out)
		}
	}
}
