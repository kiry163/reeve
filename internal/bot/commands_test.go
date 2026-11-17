package bot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kiry163/reeve/internal/builtin"
)

func readStream(t *testing.T, b *Bot) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(b.cfg.DataDir, "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// 命令在 Send 入口就被处理：不建消息、不叫醒聊天层、不调用模型，结果写成 command.result 事件。
func TestSendCommandDoesNotWakeChat(t *testing.T) {
	ai := &fakeAI{agent: blockingAgent}
	b := newBot(t, t.TempDir(), ai)
	b.Start()
	defer b.Close()

	m, err := b.Send("/help")
	if err != nil {
		t.Fatal(err)
	}
	if m.Role != "notice" || !strings.Contains(m.Text, "/env") {
		t.Fatalf("命令结果应作为 notice 返回：%+v", m)
	}
	time.Sleep(200 * time.Millisecond)

	ai.mu.Lock()
	calls := ai.calls
	ai.mu.Unlock()
	if calls != 0 {
		t.Fatalf("命令不应触发模型调用，calls=%d", calls)
	}
	if len(b.store.Unhandled()) != 0 {
		t.Fatal("命令不应留下未处理消息")
	}
	for _, m := range b.store.Messages() {
		if m.Role == "user" || m.Role == "assistant" {
			t.Fatalf("命令不应产生聊天消息：%+v", m)
		}
	}
	if h := b.store.History(10); len(h) != 0 {
		t.Fatalf("命令结果不应进入上下文：%+v", h)
	}
	raw := readStream(t, b)
	if !strings.Contains(raw, `"kind":"command.result"`) {
		t.Fatal("应写入 command.result 事件")
	}
	if !strings.Contains(raw, `"command":"/help"`) || !strings.Contains(raw, `"ok":true`) {
		t.Fatalf("command.result 事件内容不对：%s", raw)
	}
}

func TestUnknownCommandReturnsHelp(t *testing.T) {
	b := newBot(t, t.TempDir(), &fakeAI{agent: blockingAgent})
	defer b.Close()
	res, ok := b.CommandLine(context.Background(), "/nope")
	if !ok {
		t.Fatal("/nope 应被识别为命令")
	}
	if res.OK {
		t.Fatal("未知命令的 OK 应为 false")
	}
	if !strings.Contains(res.Text, "/help") {
		t.Fatalf("未知命令应提示 /help：%q", res.Text)
	}
	if raw := readStream(t, b); !strings.Contains(raw, `"ok":false`) {
		t.Fatalf("command.result 应记录 ok=false：%s", raw)
	}
}

func TestEnvCommandWritesInstanceLevel(t *testing.T) {
	b := newBot(t, t.TempDir(), &fakeAI{agent: blockingAgent})
	defer b.Close()

	res, ok := b.CommandLine(context.Background(), "/env set A=1")
	if !ok || !res.OK {
		t.Fatalf("/env set A=1 失败：%+v", res)
	}
	vars, err := b.InstanceEnv()
	if err != nil {
		t.Fatal(err)
	}
	if vars["A"] != "1" {
		t.Fatalf("实例级环境变量缺少 A：%v", vars)
	}
	raw := readStream(t, b)
	if !strings.Contains(raw, `"source":"user"`) || !strings.Contains(raw, `"name":"A"`) {
		t.Fatalf("缺少 source=user 的审计事件：%s", raw)
	}

	res, ok = b.CommandLine(context.Background(), "/env set B 2")
	if !ok || !res.OK {
		t.Fatalf("/env set B 2 失败：%+v", res)
	}
	if vars, _ = b.InstanceEnv(); vars["B"] != "2" {
		t.Fatalf("实例级环境变量缺少 B：%v", vars)
	}

	if res, ok = b.CommandLine(context.Background(), "/env unset A"); !ok || !res.OK {
		t.Fatalf("/env unset A 失败：%+v", res)
	}
	if vars, _ = b.InstanceEnv(); vars["A"] != "" {
		t.Fatalf("unset 后实例级仍有 A：%v", vars)
	}
}

func TestClearEmptiesHistoryAndKeepsChatWorking(t *testing.T) {
	b := newBot(t, t.TempDir(), &fakeAI{agent: blockingAgent})
	b.Start()
	defer b.Close()

	if _, err := b.Send("做完了吗"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first reply", func() bool { return countMsgs(b.store, "assistant", "没有任务记录") == 1 })

	res, ok := b.CommandLine(context.Background(), "/clear")
	if !ok || !res.OK {
		t.Fatalf("/clear 失败：%+v", res)
	}
	if h := b.store.History(100); len(h) != 0 {
		t.Fatalf("/clear 后上下文应为空：%+v", h)
	}

	if _, err := b.Send("做完了吗"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "reply after clear", func() bool { return countMsgs(b.store, "assistant", "没有任务记录") == 2 })
}

func TestStopCommandReportsStoppedTasks(t *testing.T) {
	b := newBot(t, t.TempDir(), &fakeAI{agent: blockingAgent})
	b.Start()
	defer b.Close()

	id, err := b.StartTask("长任务", "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "task running", func() bool {
		task, _ := b.store.Task(id)
		return task.Status == "running"
	})

	res, ok := b.CommandLine(context.Background(), "/stop")
	if !ok || !res.OK {
		t.Fatalf("/stop 失败：%+v", res)
	}
	if !strings.Contains(res.Text, "1") {
		t.Fatalf("/stop 应报告停掉 1 个任务：%q", res.Text)
	}
	waitFor(t, "task cancelled", func() bool {
		task, _ := b.store.Task(id)
		return task.Status == "cancelled"
	})
}

func TestApproveCommandResolvesApproval(t *testing.T) {
	b := newBot(t, t.TempDir(), &fakeAI{agent: blockingAgent})
	defer b.Close()

	wait := b.approver.Register(builtin.ApprovalRequest{
		ID: "appr_cmd", TaskID: "task_x", ToolName: "shell", CreatedAt: time.Now(),
	})
	decided := make(chan builtin.UserDecision, 1)
	go func() {
		d, err := wait(context.Background())
		if err != nil {
			t.Errorf("等待审批失败: %v", err)
		}
		decided <- d
	}()

	res, ok := b.CommandLine(context.Background(), "/approve appr_cmd allow")
	if !ok || !res.OK {
		t.Fatalf("/approve 失败：%+v", res)
	}
	select {
	case d := <-decided:
		if !d.Approve || d.Scope != builtin.ScopeOnce {
			t.Fatalf("审批决定 = %+v，期望 allow/once", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("审批没有收到决定")
	}
}

func TestModelAndAgentCommands(t *testing.T) {
	b := newBot(t, t.TempDir(), &fakeAI{agent: blockingAgent})
	defer b.Close()

	res, ok := b.CommandLine(context.Background(), "/model")
	if !ok || !res.OK || !strings.Contains(res.Text, "chat") {
		t.Fatalf("/model 应返回当前模型：%+v", res)
	}
	res, ok = b.CommandLine(context.Background(), "/agent")
	if !ok || !res.OK || !strings.Contains(res.Text, "builtin") {
		t.Fatalf("/agent 应返回 builtin：%+v", res)
	}
	res, ok = b.CommandLine(context.Background(), "/agent codex")
	if !ok || res.OK || !strings.Contains(res.Text, "白名单") {
		t.Fatalf("白名单外的执行器应被拒绝：%+v", res)
	}
}

func TestModelCommandSwitchesConfiguredModel(t *testing.T) {
	b := newBot(t, t.TempDir(), &fakeAI{agent: blockingAgent})
	defer b.Close()

	if _, err := b.pool.save([]APIChannel{{ID: "p", Name: "P", BaseURL: "https://example.com/v1",
		Enabled: true, Models: []string{"chat", "other"}}}); err != nil {
		t.Fatal(err)
	}
	res, ok := b.CommandLine(context.Background(), "/model other")
	if !ok || !res.OK || !strings.Contains(res.Text, "other") {
		t.Fatalf("/model other 失败：%+v", res)
	}
	if cur := b.GetSettings(); cur.Model != "other" || cur.DefaultProviderID != "p" {
		t.Fatalf("模型选择没有写回：%+v", cur)
	}

	res, ok = b.CommandLine(context.Background(), "/model nope")
	if !ok || res.OK || !strings.Contains(res.Text, "nope") {
		t.Fatalf("未知模型应失败：%+v", res)
	}
}

func TestAgentBuiltinClearsConfiguredCLI(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "agent-connection.json"), []byte(`{"cli":"pi"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b := newBot(t, dir, &fakeAI{agent: blockingAgent})
	defer b.Close()

	if cli := b.selectedCLI(); cli != "pi" {
		t.Fatalf("前置条件失败：selectedCLI = %q，期望 pi", cli)
	}
	res, ok := b.CommandLine(context.Background(), "/agent builtin")
	if !ok || !res.OK || !strings.Contains(res.Text, "builtin") {
		t.Fatalf("/agent builtin 失败：%+v", res)
	}
	if cli := b.selectedCLI(); cli != "" {
		t.Fatalf("切回内置后 selectedCLI 应为空，得到 %q", cli)
	}
	var saved Executors
	raw, err := os.ReadFile(filepath.Join(dataDir, "executors.json"))
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &saved) != nil || saved.Default != "builtin" || !slices.Contains(saved.Allowed, "pi") {
		t.Fatalf("执行器设置没有写回文件：%s", raw)
	}
	id, err := b.StartTask("内置任务", "")
	if err != nil {
		t.Fatal(err)
	}
	task, ok := b.store.Task(id)
	if !ok || task.Executor != "" {
		t.Fatalf("新任务应走内置执行器：%+v", task)
	}
}

func TestClearDismissesPendingCards(t *testing.T) {
	b := newBot(t, t.TempDir(), &fakeAI{agent: blockingAgent})
	defer b.Close()

	b.store.Append("message", Message{ID: "msg_q", Role: "assistant", Text: "选哪个？",
		Card:      &Card{ID: "card_1", Question: "选哪个？", Options: []string{"a", "b"}, Status: "pending"},
		CreatedAt: time.Now()}, false)
	b.store.Append("message", Message{ID: "msg_s", Role: "assistant", Text: "填密钥",
		Secret: &SecretReq{ID: "sec_1", Name: "TOKEN", Status: "pending"}, CreatedAt: time.Now()}, false)
	if _, ok := b.store.PendingCard(); !ok {
		t.Fatal("前置条件失败：应有未回答的卡片")
	}

	if res, ok := b.CommandLine(context.Background(), "/clear"); !ok || !res.OK {
		t.Fatalf("/clear 失败：%+v", res)
	}
	if c, ok := b.store.PendingCard(); ok {
		t.Fatalf("/clear 后不应还有未回答的卡片：%+v", c)
	}
	if sc := b.store.PendingSecretCards(); len(sc) != 0 {
		t.Fatalf("/clear 后不应还有待填写的密钥卡片：%+v", sc)
	}
	if c, ok := b.store.Card("card_1"); !ok || c.Status != "dismissed" {
		t.Fatalf("卡片应置为 dismissed：%+v", c)
	}
	// 密钥卡的状态集是 pending|saved|cancelled：/clear 用 cancelled，网页才不会卡在未知状态。
	if s, ok := b.store.SecretCard("sec_1"); !ok || s.Status != "cancelled" {
		t.Fatalf("密钥卡片应置为 cancelled：%+v", s)
	}
}

func TestClearKeepsUnhandledMessagesAndWritesOneEvent(t *testing.T) {
	b := newBot(t, t.TempDir(), &fakeAI{agent: blockingAgent})
	defer b.Close()

	b.store.Append("message", Message{ID: "msg_done", Role: "user", Text: "旧的", CreatedAt: time.Now()}, false)
	b.store.Append("message.handled", []string{"msg_done"}, true)
	b.store.Append("message", Message{ID: "msg_reply", Role: "assistant", Text: "旧回复", CreatedAt: time.Now()}, false)
	b.store.Append("message", Message{ID: "msg_pending", Role: "user", Text: "排队的", CreatedAt: time.Now()}, false)

	if res, ok := b.CommandLine(context.Background(), "/clear"); !ok || !res.OK {
		t.Fatalf("/clear 失败：%+v", res)
	}
	unhandled := b.store.Unhandled()
	if len(unhandled) != 1 || unhandled[0].ID != "msg_pending" {
		t.Fatalf("未处理消息应保留：%+v", unhandled)
	}
	if h := b.store.History(100); len(h) != 0 {
		t.Fatalf("已处理消息应被隐藏：%+v", h)
	}
	if n := strings.Count(readStream(t, b), `"kind":"message.hidden"`); n != 1 {
		t.Fatalf("应只写一条 message.hidden 事件，实际 %d 条", n)
	}
}

// /clear 作废正在执行的这一轮，但保留排队中的输入；旧批次不能被重新跑一遍。
func TestClearDoesNotReplayActiveBatch(t *testing.T) {
	ai := &fakeAI{agent: blockingAgent, chatBlock: make(chan struct{})}
	b := newBot(t, t.TempDir(), ai)
	b.Start()
	defer b.Close()

	if _, err := b.Send("第一条"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first chat call", func() bool {
		ai.mu.Lock()
		defer ai.mu.Unlock()
		return ai.calls >= 1
	})
	firstID := userMessageID(t, b, "第一条")

	if _, err := b.Send("做完了吗"); err != nil {
		t.Fatal(err)
	}
	secondID := userMessageID(t, b, "做完了吗")

	if res, ok := b.CommandLine(context.Background(), "/clear"); !ok || !res.OK {
		t.Fatalf("/clear 失败：%+v", res)
	}
	first, ok := b.store.Message(firstID)
	if !ok || !first.Hidden || !first.handled {
		t.Fatalf("当前回合的输入应被隐藏并标记处理：%+v", first)
	}
	unhandled := b.store.Unhandled()
	if len(unhandled) != 1 || unhandled[0].ID != secondID {
		t.Fatalf("排队消息应保持未处理：%+v", unhandled)
	}

	// 排队消息会开新回合；旧批次不能再触发一次模型调用。
	waitFor(t, "second chat call", func() bool {
		ai.mu.Lock()
		defer ai.mu.Unlock()
		return ai.calls == 2
	})
	time.Sleep(200 * time.Millisecond)
	ai.mu.Lock()
	calls, reqs := ai.calls, append([]string(nil), ai.reqs...)
	ai.mu.Unlock()
	if calls != 2 {
		t.Fatalf("旧批次不应被重放：calls=%d", calls)
	}
	last := reqs[len(reqs)-1]
	if strings.Contains(last, "第一条") || !strings.Contains(last, "做完了吗") {
		t.Fatalf("新回合的输入应只包含排队消息：%s", last)
	}
}

func userMessageID(t *testing.T, b *Bot, text string) string {
	t.Helper()
	for _, m := range b.store.Messages() {
		if m.Role == "user" && m.Text == text {
			return m.ID
		}
	}
	t.Fatalf("没有找到用户消息 %q", text)
	return ""
}

func TestContextTasksAndSkillsCommands(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "data", "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: demo\ndescription: 演示技能\nwhen_to_use: 测试时\n---\n正文\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
	b := newBot(t, dir, &fakeAI{agent: blockingAgent})
	defer b.Close()

	b.store.Append("task.created", TaskRec{ID: "task_demo", Title: "演示任务", Status: "completed",
		CreatedAt: time.Now(), EndedAt: time.Now()}, false)
	b.store.Append("message", Message{ID: "msg_ctx", Role: "user", Text: "你好", CreatedAt: time.Now()}, false)
	b.store.Append("message.handled", []string{"msg_ctx"}, true)

	for _, tc := range []struct {
		line string
		want string
	}{
		{"/tasks", "task_demo"},
		{"/skills", "demo"},
		{"/context", "1 条消息"},
	} {
		res, ok := b.CommandLine(context.Background(), tc.line)
		if !ok || !res.OK {
			t.Fatalf("%s 失败：%+v", tc.line, res)
		}
		if !strings.Contains(res.Text, tc.want) {
			t.Fatalf("%s 结果缺少 %q：%q", tc.line, tc.want, res.Text)
		}
	}
}
