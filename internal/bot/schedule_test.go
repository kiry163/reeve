package bot

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiry163/reeve/internal/builtin"
	"github.com/kiry163/reeve/internal/scheduler"
)

type fixedClock struct{ t time.Time }

func (c *fixedClock) Now() time.Time { return c.t }

func TestSchedulerDispatchCreatesQueuedTask(t *testing.T) {
	dir := t.TempDir()
	ai := &fakeAI{agent: func(_ context.Context, _ []builtin.Message) (builtin.Message, error) {
		return builtin.Message{Content: "agent done"}, nil
	}}
	b, err := New(Config{
		Name: "Reeve", DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws"),
		ChatProvider: ai, ChatModel: "chat", AgentProvider: ai, AgentModel: "agent",
		Policy: builtin.PolicyOnRequest, ChatPolicy: builtin.PolicyUntrusted, Demo: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	loc, _ := time.LoadLocation(scheduler.DefaultTimezone)
	fc := &fixedClock{t: time.Date(2026, 11, 9, 8, 59, 0, 0, loc)}
	b.sched.Clock = fc
	p, err := b.sched.Upsert(scheduler.Draft{
		Title: "早报", Prompt: "写一段早报", Enabled: true,
		Spec: scheduler.Spec{Kind: scheduler.Once, Timezone: scheduler.DefaultTimezone,
			AtLocal: time.Date(2026, 11, 9, 9, 0, 0, 0, loc)},
	})
	if err != nil {
		t.Fatal(err)
	}
	fc.t = time.Date(2026, 11, 9, 9, 0, 1, 0, loc)
	b.sched.Tick()
	tasks := b.store.Tasks()
	if len(tasks) != 1 {
		t.Fatalf("tasks=%d", len(tasks))
	}
	if tasks[0].Status != "queued" && tasks[0].Status != "running" && tasks[0].Status != "completed" {
		t.Fatalf("status=%s", tasks[0].Status)
	}
	due := time.Date(2026, 11, 9, 9, 0, 0, 0, loc)
	if _, ok := b.sched.Store.Claim(scheduler.OccurrenceKey(p.ID, due)); !ok {
		t.Fatal("missing claim")
	}
	b.sched.Tick()
	if len(b.store.Tasks()) != 1 {
		t.Fatal("double task")
	}
}

func TestScheduleCreateRequiresApproval(t *testing.T) {
	dir := t.TempDir()
	ai := &fakeAI{agent: func(_ context.Context, _ []builtin.Message) (builtin.Message, error) {
		return builtin.Message{Content: "ok"}, nil
	}}
	b, err := New(Config{
		Name: "Reeve", DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws"),
		ChatProvider: ai, ChatModel: "chat", AgentProvider: ai, AgentModel: "agent",
		Policy: builtin.PolicyOnRequest, ChatPolicy: builtin.PolicyUntrusted, Demo: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	args, _ := json.Marshal(map[string]any{
		"title": "提醒", "prompt": "说一声", "kind": "daily", "hour": 9, "minute": 0,
	})
	out, err := b.scheduleMutate("create", args, func() (string, error) {
		p, err := b.sched.Upsert(scheduler.Draft{Title: "提醒", Prompt: "说一声", Enabled: true,
			Spec: scheduler.Spec{Kind: scheduler.Daily, Hour: 9, Minute: 0}})
		if err != nil {
			return "", err
		}
		return p.ID, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "需要用户确认") {
		t.Fatalf("expected approval gate, got %q", out)
	}
	if len(b.Schedules()) != 0 {
		t.Fatalf("must not persist before approval: %v", b.Schedules())
	}
}

func TestScheduleHTTPListAndPause(t *testing.T) {
	dir := t.TempDir()
	ai := &fakeAI{agent: func(_ context.Context, _ []builtin.Message) (builtin.Message, error) {
		return builtin.Message{Content: "ok"}, nil
	}}
	b, err := New(Config{
		Name: "Reeve", DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws"),
		ChatProvider: ai, ChatModel: "chat", AgentProvider: ai, AgentModel: "agent",
		Policy: builtin.PolicyOnRequest, ChatPolicy: builtin.PolicyUntrusted, Demo: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	_, err = b.sched.Upsert(scheduler.Draft{Title: "HTTP", Prompt: "x", Enabled: true,
		Spec: scheduler.Spec{Kind: scheduler.Daily, Hour: 10, Minute: 0}})
	if err != nil {
		t.Fatal(err)
	}
	list := b.Schedules()
	if len(list) != 1 || list[0]["title"] != "HTTP" {
		t.Fatalf("%v", list)
	}
	id := list[0]["id"].(string)
	p, err := b.sched.SetEnabled(id, false)
	if err != nil || p.Enabled {
		t.Fatal(err, p.Enabled)
	}
}

// 定时计划固定派生内置任务：即使默认执行器是外部 CLI，到点也不能自动启动外部进程。
// 手动「立刻运行」（POST /api/schedules/{id}/run）走的是同一个 Dispatch。
func TestScheduledTaskAlwaysUsesBuiltin(t *testing.T) {
	b := newBotWith(t, t.TempDir(), &fakeAI{agent: func(context.Context, []builtin.Message) (builtin.Message, error) {
		return builtin.Message{Content: "ok"}, nil
	}}, nil)
	defer b.Close()
	if err := b.SaveExecutors(Executors{Allowed: []string{"builtin", "omp"}, Default: "omp"}); err != nil {
		t.Fatal(err)
	}
	if b.DefaultExecutor() != "omp" {
		t.Fatalf("前置条件：默认执行器应为 omp，得到 %q", b.DefaultExecutor())
	}
	p, err := b.sched.Upsert(scheduler.Draft{Title: "内部", Prompt: "写一行字", Enabled: true,
		Spec: scheduler.Spec{Kind: scheduler.Daily, Hour: 9, Minute: 0}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := b.sched.RunNow(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := b.store.Task(c.TaskID)
	if !ok {
		t.Fatalf("计划派发后没有任务记录：%s", c.TaskID)
	}
	if rec.Executor != "" {
		t.Fatalf("定时任务必须用内置执行器（空串），得到 %q", rec.Executor)
	}
}
