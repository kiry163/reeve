package bot

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiry163/reeve/internal/builtin"
)

// 所有 Bot 合计的并发上限：3 个 Bot 各派 2 个任务，上限 2 时同时只有 2 个在跑；取消后其余的自动开始。
func TestGlobalConcurrencyAcrossBots(t *testing.T) {
	dir := t.TempDir()
	ai := &fakeAI{agent: blockingAgent}
	cfg := Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws"),
		ChatProvider: ai, ChatModel: "chat", AgentProvider: ai, AgentModel: "agent", MaxConcurrent: 2}
	bs, err := OpenBots(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()
	var all []*Bot
	all = append(all, bs.main)
	for _, n := range []string{"B", "C"} {
		p, err := bs.setProfile("", BotProfile{Name: n})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, bs.runtimes[p.ID])
	}
	for _, b := range all {
		for i := 0; i < 2; i++ {
			if _, err := b.StartTask("block", ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	running := func() int {
		n := 0
		for _, b := range all {
			for _, tk := range b.store.Tasks() {
				if tk.Status == "running" {
					n++
				}
			}
		}
		return n
	}
	waitFor(t, "two tasks running", func() bool { return running() == 2 })
	time.Sleep(100 * time.Millisecond)
	if n := running(); n != 2 || bs.main.slots.running() != 2 {
		t.Fatalf("global limit not enforced: %d running", n)
	}
	// 取消正在跑的，其他 Bot 排队的任务应该补上
	for _, b := range all {
		for _, tk := range b.store.Tasks() {
			if tk.Status == "running" {
				_ = b.CancelTask(tk.ID, "user")
			}
		}
	}
	waitFor(t, "queued tasks of other Bots start", func() bool {
		cancelled := 0
		for _, b := range all {
			for _, tk := range b.store.Tasks() {
				if tk.Status == "cancelled" {
					cancelled++
				}
			}
		}
		return cancelled == 2 && running() == 2
	})
}

// 彻底删除：数据目录和工作区都被删掉；普通删除保留（见 TestDeleteBotStopsWorkAndSurvivesRestart）。
func TestPurgeDeleteRemovesData(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws")}
	bs, err := OpenBots(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()
	p, err := bs.setProfile("", BotProfile{Name: "Temp"})
	if err != nil {
		t.Fatal(err)
	}
	b := bs.runtimes[p.ID]
	if _, err := b.Upload("x.txt", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	dataDir, ws := b.cfg.DataDir, b.cfg.Workspace
	h := bs.Handler(nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/bots/"+p.ID+"/paths", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), p.ID) {
		t.Fatalf("paths: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/bots/"+p.ID+"?purge=1", nil))
	if w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	waitFor(t, "data removed", func() bool {
		_, e1 := os.Stat(dataDir)
		_, e2 := os.Stat(ws)
		return os.IsNotExist(e1) && os.IsNotExist(e2)
	})
	if _, err := os.Stat(bs.main.cfg.DataDir); err != nil {
		t.Fatal("main data dir must survive")
	}
}

// 根路径上的旧 /api/stream 等单 Bot 接口不再提供；只能通过 /api/bots/{id}/…。
func TestRootLegacyAPIRemoved(t *testing.T) {
	dir := t.TempDir()
	bs, err := OpenBots(Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws")})
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()
	h := bs.Handler(nil)
	for path, want := range map[string]int{"/api/stream": 404, "/api/info": 404, "/api/bots/main/info": 200,
		"/api/settings": 200, "/api/health": 200} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Errorf("GET %s = %d, want %d", path, w.Code, want)
		}
	}
}

// 事件日志：末尾半行坏数据在打开时被截掉；agent 的流式文字不写盘。
func TestStoreTruncatesPartialLineAndSkipsTextDelta(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Append("message", Message{ID: "m1", Role: "user", Text: "hello"}, false)
	s.Close()
	path := filepath.Join(dir, "stream.jsonl")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"seq":2,"kind":"message","data":{"id":"m2"`) // 断电时写了一半
	f.Close()
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Append("message", Message{ID: "m3", Role: "user", Text: "after crash"}, false)
	s.Close()
	raw, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || strings.Contains(string(raw), `"m2"`) || !strings.Contains(lines[1], "after crash") {
		t.Fatalf("partial line not truncated:\n%s", raw)
	}
	s, _ = OpenStore(dir)
	defer s.Close()
	if n := len(s.Messages()); n != 2 {
		t.Fatalf("expected 2 messages after reopen, got %d", n)
	}

	ai := &fakeAI{agent: func(_ context.Context, _ []builtin.Message) (builtin.Message, error) {
		return builtin.Message{Content: "streamed words"}, nil
	}}
	b := newBot(t, t.TempDir(), ai)
	defer b.Close()
	b.Start()
	id, _ := b.StartTask("go", "")
	waitFor(t, "task done", func() bool { rec, _ := b.store.Task(id); return rec.Status == "completed" })
	b.store.mu.Lock()
	defer b.store.mu.Unlock()
	for _, e := range b.store.log {
		if e.Kind == "agent" && strings.Contains(string(e.Data), `"text.delta"`) {
			t.Fatal("agent text.delta was persisted")
		}
	}
}

func TestCompactorWiredByDefault(t *testing.T) {
	b := newBot(t, t.TempDir(), &fakeAI{agent: blockingAgent})
	defer b.Close()
	if _, ok := b.mgr.Agent.Compactor.(*builtin.AutoCompactor); !ok {
		t.Fatal("background agent should compact long conversations by default")
	}
}

// 停机中断不能覆盖正在进行的用户取消：运行中 + StopReason=user + 未请求暂停由取消路径收尾。
// （用户暂停同样写 StopReason=user，但停机打断它时仍要落可恢复的 interrupted，见
// TestCloseWhilePausedKeepsInterrupted。）
func TestShutdownInterruptDoesNotOverrideUserCancel(t *testing.T) {
	b := newBotWith(t, t.TempDir(), &fakeAI{}, nil)
	defer b.Close()
	b.shuttingDown.Store(true)
	b.store.Append("task.created", TaskRec{ID: "task_user_cancel", Title: "取消", Input: "x",
		Executor: "omp", Status: "running", StopReason: "user"}, false)
	if b.markInterruptedOnShutdown("task_user_cancel") {
		t.Fatal("用户的取消不该被判成停机中断")
	}
	if rec, _ := b.store.Task("task_user_cancel"); rec.Status != "running" || rec.StopReason != "user" {
		t.Fatalf("取消路径的记录被改写了：%+v", rec)
	}

	b.store.Append("task.created", TaskRec{ID: "task_shutdown_int", Title: "停机", Input: "x",
		Executor: "omp", Status: "running"}, false)
	if !b.markInterruptedOnShutdown("task_shutdown_int") {
		t.Fatal("普通运行中的任务应落 interrupted")
	}
	if rec, _ := b.store.Task("task_shutdown_int"); rec.Status != "paused" || rec.StopReason != "interrupted" {
		t.Fatalf("停机中断应落 paused/interrupted：%+v", rec)
	}
}

// 终态事件到达后必须清掉 Pausing，否则重启恢复会把已结束的任务当成“还在暂停中”而复活成 paused。
func TestTerminalEventClearsPausing(t *testing.T) {
	b := newBotWith(t, t.TempDir(), &fakeAI{}, nil)
	defer b.Close()
	const id = "task_pausing_done"
	b.store.Append("task.created", TaskRec{ID: id, Title: "t", Input: "x", Status: "running", CreatedAt: time.Now()}, false)
	b.store.Append("task.pausing", TaskRec{ID: id, StopReason: "user"}, false)
	b.store.Append("agent", builtin.Event{TaskID: id, Type: builtin.EvTaskFailed, Time: time.Now(),
		Data: builtin.Result{TaskID: id, Status: builtin.StatusFailed, Error: "boom"}}, false)
	rec, _ := b.store.Task(id)
	if rec.Pausing {
		t.Fatal("终态后 Pausing 应被清掉")
	}
	b.recover()
	rec, _ = b.store.Task(id)
	if rec.Status != "failed" {
		t.Fatalf("已结束的任务重启后不该复活：%+v", rec)
	}
}
