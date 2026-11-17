package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiry163/reeve/internal/builtin"
)

// 重启后 session 批准仍然生效（记录从事件日志重放到 Guard）。
func TestSessionApprovalSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	args := json.RawMessage(`{"command":"make deploy"}`)
	hash := builtin.ArgsHash(args)
	s, err := OpenStore(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	s.Append("task.created", TaskRec{ID: "task_a", Title: "a", Status: "running", CreatedAt: time.Now()}, false)
	s.Append("agent", builtin.Event{TaskID: "task_a", Type: builtin.EvApprovalRequested,
		Data: builtin.ApprovalRequest{ID: "appr_a", TaskID: "task_a", ToolName: "shell", Args: args}}, false)
	s.Append("agent", builtin.Event{TaskID: "task_a", Type: builtin.EvApprovalDecided,
		Data: map[string]any{"approval_id": "appr_a", "decision": "allow", "scope": "session"}}, false)
	s.Append("approval.record", builtin.ApprovalRecord{ID: "appr_a", TaskID: "task_a", ToolName: "shell",
		ArgsHash: hash, Decision: builtin.Allow, Scope: builtin.ScopeSession, CreatedAt: time.Now()}, false)
	s.Close()

	b := newBot(t, dir, &fakeAI{agent: blockingAgent})
	defer b.Close()
	if _, ok := b.guard.Consume("task_a", "shell", hash); !ok {
		t.Fatal("session approval should still be effective after restart")
	}
}

// 重启时排队中的任务也转成 paused(restart)，不会自动开跑。
func TestQueuedTaskPausedOnRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	s.Append("task.created", TaskRec{ID: "task_q", Title: "排队", Input: "do", Status: "queued", CreatedAt: time.Now()}, false)
	s.Close()

	var ran atomic.Bool
	ai := &fakeAI{agent: func(_ context.Context, _ []builtin.Message) (builtin.Message, error) {
		ran.Store(true)
		return builtin.Message{Content: "不该跑"}, nil
	}}
	b := newBot(t, dir, ai)
	defer b.Close()
	b.Start()
	tk, _ := b.store.Task("task_q")
	if tk.Status != "paused" || tk.StopReason != "restart" {
		t.Fatalf("queued task should be paused(restart) after restart: %+v", tk)
	}
	time.Sleep(150 * time.Millisecond)
	if ran.Load() {
		t.Fatal("queued task must not auto-start after restart")
	}
}

// 任务结束（用户取消）后，还挂着的审批作废；再作答返回 409「任务已结束」。
func TestApprovalOnEndedTaskReturns409(t *testing.T) {
	dir := t.TempDir()
	ai := &fakeAI{agent: func(_ context.Context, ms []builtin.Message) (builtin.Message, error) {
		if toolSteps(ms) == 0 {
			return toolCall("shell", map[string]string{"command": "rm -rf junk"}), nil
		}
		return builtin.Message{Content: "完成"}, nil
	}}
	b := newBot(t, dir, ai)
	b.Start()
	defer b.Close()
	srv := httptest.NewServer(b.Handler(""))
	defer srv.Close()

	id, err := b.StartTask("清理 junk", "清理")
	if err != nil {
		t.Fatal(err)
	}
	var appr ApprovalRec
	waitFor(t, "approval", func() bool {
		p := b.store.PendingApprovals()
		if len(p) == 1 {
			appr = p[0]
		}
		return len(p) == 1
	})
	if err := b.CancelTask(id, "user"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "cancelled", func() bool { tk, _ := b.store.Task(id); return tk.Status == "cancelled" })
	if a, _ := b.store.Approval(appr.ID); a.Status == "pending" {
		t.Fatalf("pending approval should be expired when the task ends: %+v", a)
	}
	resp, err := http.Post(srv.URL+"/api/approvals/"+appr.ID, "application/json", strings.NewReader(`{"decision":"once"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("answering an approval of an ended task should be 409, got %d", resp.StatusCode)
	}
	var body struct{ Error string }
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if !strings.Contains(body.Error, "任务已结束") {
		t.Fatalf("error message: %q", body.Error)
	}
}

// set_env 的审批通知会进模型上下文：value 必须换成占位符；
// 事件里的审批记录保持原样，网页卡片仍能看到值。
func TestApprovalNoticeRedactsSetEnvValue(t *testing.T) {
	b := newBot(t, t.TempDir(), nil)
	defer b.Close()
	const taskID = "task_env_approval"
	b.store.Append("task.created", TaskRec{ID: taskID, Title: "设置环境变量", Status: "running", CreatedAt: time.Now()}, false)
	const secretValue = "plain-value-not-in-context"
	args := json.RawMessage(`{"name":"HTTPS_PROXY","value":"` + secretValue + `"}`)
	b.onAgentEvent(builtin.Event{TaskID: taskID, Type: builtin.EvApprovalRequested, Time: time.Now(),
		Data: builtin.ApprovalRequest{ID: "appr_env", TaskID: taskID, ToolName: "set_env", Args: args}})

	notices := 0
	for _, m := range b.store.Messages() {
		if m.Role == "notice" && strings.Contains(m.Text, "需要用户批准") {
			notices++
			if strings.Contains(m.Text, secretValue) {
				t.Fatalf("审批通知不应包含环境变量值：%q", m.Text)
			}
			if !strings.Contains(m.Text, "<已隐藏>") {
				t.Fatalf("审批通知应显示占位符：%q", m.Text)
			}
		}
	}
	if notices != 1 {
		t.Fatalf("应有一条审批通知，得到 %d", notices)
	}
	a, ok := b.store.Approval("appr_env")
	if !ok || !strings.Contains(string(a.Args), secretValue) {
		t.Fatalf("审批记录应保留真实值供卡片展示：%+v", a)
	}
}

// 重启后删聊天层发起的审批：决定会补记，但那次操作没有执行者，必须明确告诉用户。
func TestChatApprovalAfterRestartDoesNotExecute(t *testing.T) {
	dir := t.TempDir()
	ai := &fakeAI{agent: blockingAgent}
	b0 := newBot(t, dir, ai)
	if err := b0.vault.Set("API_TOKEN", "tok_keep", "测试用", nil); err != nil {
		t.Fatal(err)
	}
	b0.Close()

	s, err := OpenStore(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	s.Append("agent", builtin.Event{TaskID: "chat_dead", Type: builtin.EvApprovalRequested,
		Data: builtin.ApprovalRequest{ID: "appr_chat", TaskID: "chat_dead", ToolName: "delete_secret",
			Args: json.RawMessage(`{"name":"API_TOKEN"}`)}}, false)
	s.Close()

	b := newBot(t, dir, ai)
	defer b.Close()
	b.Start()
	if a, _ := b.store.Approval("appr_chat"); a.Status != "pending" {
		t.Fatalf("chat approval should survive restart: %+v", a)
	}
	if err := b.Decide("appr_chat", "once"); err != nil {
		t.Fatal(err)
	}
	if !b.vault.Has("API_TOKEN") {
		t.Fatal("a restarted chat approval must not execute the operation")
	}
	waitFor(t, "explain missing execution", func() bool { return countMsgs(b.store, "notice", "补记了你的批准") == 1 })
}
