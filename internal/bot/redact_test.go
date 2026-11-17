package bot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiry163/reeve/internal/auth"
	"github.com/kiry163/reeve/internal/builtin"
)

func newBotWithToken(t *testing.T, dir string, ai *fakeAI) (*Bot, string) {
	t.Helper()
	tok, _, err := auth.EnsureToken(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	return newBot(t, dir, ai), tok
}

// 用户把配对串或裸 token 粘进聊天：持久事件与 History 里都不能有明文。
func TestPairStringAndBareTokenNeverPersisted(t *testing.T) {
	dir := t.TempDir()
	b, tok := newBotWithToken(t, dir, &fakeAI{agent: blockingAgent})
	defer b.Close()

	pair := "reeve://pair?v=1&host=192.168.1.20:18800&token=" + tok
	m, err := b.Send("这是我的配对串 " + pair)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.Text, tok) {
		t.Fatalf("returned message leaks token: %q", m.Text)
	}
	if _, err := b.Send("裸 token：" + tok); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "data", "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), tok) {
		t.Fatal("stream.jsonl contains the access token")
	}
	for _, m := range b.store.History(20) {
		if strings.Contains(m.Text, tok) {
			t.Fatalf("History leaks token: %q", m.Text)
		}
	}
	if n := countMsgs(b.store, "notice", "疑似密钥"); n == 0 {
		t.Error("pasting the token should prompt the chat layer to use a secret card")
	}
}

// 之后轮换 token：新的 token 也会被脱敏（每次重新从 token 文件读）。
func TestRotatedTokenIsRedacted(t *testing.T) {
	dir := t.TempDir()
	b, old := newBotWithToken(t, dir, &fakeAI{agent: blockingAgent})
	defer b.Close()
	next, err := auth.RotateToken(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if b.redactText("新 token "+next) == "新 token "+next {
		t.Fatal("rotated token must be redacted without restarting")
	}
	_ = old
}

// shell 输出里出现 token：发给模型的工具结果必须是掩码。
func TestShellOutputRedactedBeforeModel(t *testing.T) {
	dir := t.TempDir()
	tok, _, err := auth.EnsureToken(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	ai := &fakeAI{agent: func(_ context.Context, ms []builtin.Message) (builtin.Message, error) {
		if toolSteps(ms) == 0 {
			return toolCall("shell", map[string]string{"command": "printf '%s\\n' '" + tok + "'"}), nil
		}
		return builtin.Message{Content: "读完了"}, nil
	}}
	b := newBot(t, dir, ai)
	defer b.Close()
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
				if strings.Contains(m.Content, tok) {
					t.Fatalf("tool result fed to the model leaks token: %q", m.Content)
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
	b, tok := newBotWithToken(t, dir, &fakeAI{agent: blockingAgent})
	defer b.Close()
	b.saveCheckpoint("task_x", map[string]any{"messages": []builtin.Message{{Role: "user", Content: "tok=" + tok}}, "steps": 1})
	raw, err := os.ReadFile(b.checkpointPath("task_x"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), tok) {
		t.Fatal("checkpoint file contains the access token")
	}
	cp, ok := b.loadCheckpoint("task_x")
	if !ok || strings.Contains(cp.Messages[0].Content, tok) || !strings.Contains(cp.Messages[0].Content, tokenMask) {
		t.Fatalf("loaded checkpoint: %+v", cp)
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
	b, tok := newBotWithToken(t, dir, &fakeAI{agent: blockingAgent})
	defer b.Close()
	b.store.Append("task.created", TaskRec{ID: "task_leak", Title: "泄漏", Input: "无害", Status: "paused", CreatedAt: time.Now()}, false)
	b.store.mu.Lock()
	if tsk := b.store.tasks["task_leak"]; tsk != nil {
		tsk.Summary = "token " + tok
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
		if strings.Contains(out, tok) {
			t.Fatalf("%s leaks token: %q", name, out)
		}
	}
}
