//go:build !windows

package bot

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiry163/reeve/internal/exec"
	"github.com/kiry163/reeve/internal/exec/cli"
	"github.com/kiry163/reeve/internal/scheduler"
)

// 外部任务没有任何事件（也没有心跳）：超过 StallTimeout 后监工发 SIGSTOP，
// 落 task.note + task.pausing{stall}，任务转 paused(stall)，并发槽继续占着（进程还活着）。
func TestStallPausesExternalTask(t *testing.T) {
	b, _ := externalBot(t, "omp", cli.Options{})
	b.cfg.StallTimeout = 300 * time.Millisecond
	b.cfg.ExternalStallTimeout = 300 * time.Millisecond
	h := &stubHandle{done: make(chan exec.Result, 1)}
	b.execs = exec.NewRegistry(stubRunner{kind: exec.KindOMP, h: h})
	// Close 会等 startExternal 的收尾 goroutine；测试中途失败时也要给它一个结果。
	t.Cleanup(func() {
		select {
		case h.done <- exec.Result{Status: "cancelled"}:
		default:
		}
	})
	startWatchStalls(b)
	id, err := b.CreateTask("外部任务卡住", "", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "external task running", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "running"
	})
	waitFor(t, "paused by stall", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "paused" && rec.StopReason == "stall"
	})
	pauseN, stopN := h.counts()
	if pauseN != 1 || stopN != 0 {
		t.Fatalf("卡死时应对进程组发一次 SIGSTOP（Pause），实际 pause=%d stop=%d", pauseN, stopN)
	}
	rec, _ := b.store.Task(id)
	if rec.Pausing {
		t.Fatalf("暂停落定后不该还停在 pausing：%+v", rec)
	}
	if note := strings.Join(rec.Notes, "\n"); !strings.Contains(note, "没有任何输出") {
		t.Fatalf("卡死应留一条 task.note，实际 %q", note)
	}
	if n := b.slots.running(); n != 1 {
		t.Fatalf("挂起的外部进程仍占着并发槽，期望 1，实际 %d", n)
	}
	// task.pausing 与 task.paused 是两次写入：状态变 paused 时事件可能还没落，等它落定再数。
	var pausing, paused int
	waitFor(t, "stall events", func() bool {
		pausing, paused = 0, 0
		for _, e := range b.store.Since(0) {
			switch e.Kind {
			case "task.pausing":
				var p TaskRec
				if json.Unmarshal(e.Data, &p) == nil && p.ID == id && p.StopReason == "stall" {
					pausing++
				}
			case "agent":
				var ev persistedAgent
				if json.Unmarshal(e.Data, &ev) == nil && ev.TaskID == id && ev.Type == "task.paused" {
					paused++
				}
			}
		}
		return pausing >= 1 && paused >= 1
	})
	if pausing != 1 {
		t.Fatalf("卡死应落一条 task.pausing{stall}，实际 %d", pausing)
	}
	if paused != 1 {
		t.Fatalf("外部任务卡死应由监工补一条 task.paused，实际 %d（pause=%d stop=%d，事件 %+v）", paused, pauseN, stopN, agentEvents(b))
	}
	h.done <- exec.Result{Status: "cancelled"}
	waitFor(t, "terminal after stall pause", func() bool {
		rec, ok := b.store.Task(id)
		return ok && terminal(rec.Status)
	})
}

// 有心跳（Proc 每 15 秒 Touch 一次）就不是卡死：把 StallTimeout 调小后持续注入心跳也不暂停。
func TestHeartbeatKeepsExternalTaskAlive(t *testing.T) {
	b, _ := externalBot(t, "omp", cli.Options{})
	b.cfg.StallTimeout = 250 * time.Millisecond
	b.cfg.ExternalStallTimeout = time.Minute
	h := &stubHandle{done: make(chan exec.Result, 1)}
	b.execs = exec.NewRegistry(stubRunner{kind: exec.KindOMP, h: h})
	// Close 会等 startExternal 的收尾 goroutine；测试中途失败时也要给它一个结果。
	t.Cleanup(func() {
		select {
		case h.done <- exec.Result{Status: "cancelled"}:
		default:
		}
	})
	startWatchStalls(b)
	id, err := b.CreateTask("有心跳的外部任务", "", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "external task running", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "running" && !rec.LastProgress.IsZero()
	})
	start, _ := b.store.Task(id)
	progressAtStart := start.LastProgress
	deadline := time.Now().Add(600 * time.Millisecond)
	for time.Now().Before(deadline) {
		b.emitExecEvent(id, exec.Event{Kind: exec.EventHeartbeat})
		time.Sleep(40 * time.Millisecond)
	}
	if p, _ := h.counts(); p != 0 {
		t.Fatalf("有心跳的任务不该被 SIGSTOP，pause=%d", p)
	}
	rec, _ := b.store.Task(id)
	if rec.Status != "running" || rec.Pausing {
		t.Fatalf("有心跳的任务应继续运行：%+v", rec)
	}
	if !rec.LastProgress.Equal(progressAtStart) {
		t.Fatalf("心跳不该被算成进展：LastProgress %v -> %v", progressAtStart, rec.LastProgress)
	}
	if !rec.LastActivity.After(progressAtStart) {
		t.Fatalf("心跳应刷新 LastActivity：%v", rec.LastActivity)
	}
	h.done <- exec.Result{Status: "completed", Output: "好了"}
	waitFor(t, "completed", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "completed"
	})
}

// 重启：外部任务标 paused/interrupted（不自动重跑），内置任务仍是 paused/restart；通知文案分开。
func TestRecoverMarksExternalTaskInterrupted(t *testing.T) {
	dir := t.TempDir()
	b1 := newBotWith(t, dir, nil, nil)
	b1.store.Append("task.created", TaskRec{ID: "task_ext", Title: "跑外部", Input: "跑", Executor: "omp",
		Status: "running", CreatedAt: time.Now()}, false)
	b1.store.Append("task.created", TaskRec{ID: "task_in", Title: "跑内置", Input: "跑",
		Status: "running", CreatedAt: time.Now()}, false)
	b1.Close() // 模拟进程直接退出

	b2 := newBotWith(t, dir, nil, nil) // 重启
	defer b2.Close()
	ext, ok := b2.store.Task("task_ext")
	if !ok {
		t.Fatal("外部任务丢失")
	}
	if ext.Status != "paused" || ext.StopReason != "interrupted" {
		t.Fatalf("重启后外部任务应为 paused/interrupted，得到 %s/%s", ext.Status, ext.StopReason)
	}
	in, _ := b2.store.Task("task_in")
	if in.Status != "paused" || in.StopReason != "restart" {
		t.Fatalf("重启后内置任务应为 paused/restart，得到 %s/%s", in.Status, in.StopReason)
	}

	// 通知文案：外部说清「已中断 / 不会自动重跑 / 重新派一次」，内置仍是「可以继续」。
	var extNote, inNote string
	for _, text := range noticeTexts(b2) {
		if strings.Contains(text, "task_ext") {
			extNote = text
		}
		if strings.Contains(text, "task_in") && !strings.Contains(text, "task_ext") {
			inNote = text
		}
	}
	for _, want := range []string{"已中断", "不会自动重跑", "重新派"} {
		if !strings.Contains(extNote, want) {
			t.Fatalf("外部任务的重启通知应包含 %q，实际 %q", want, extNote)
		}
	}
	if !strings.Contains(inNote, "可以继续") {
		t.Fatalf("内置任务的重启通知应说明可以继续，实际 %q", inNote)
	}

	// 中断 ≠ 重跑：恢复调度也不会新建任务或发出新的 task.started。
	b2.schedule()
	time.Sleep(300 * time.Millisecond)
	created, started := map[string]int{}, map[string]int{}
	for _, e := range b2.store.Since(0) {
		switch e.Kind {
		case "task.created":
			var rec TaskRec
			if json.Unmarshal(e.Data, &rec) == nil {
				created[rec.ID]++
			}
		case "agent":
			var ev persistedAgent
			if json.Unmarshal(e.Data, &ev) == nil && ev.Type == "task.started" {
				started[ev.TaskID]++
			}
		}
	}
	for _, id := range []string{"task_ext", "task_in"} {
		if created[id] != 1 {
			t.Fatalf("%s 被重复派发：task.created %d 条", id, created[id])
		}
		if started[id] != 0 {
			t.Fatalf("%s 中断后不该自动重跑：task.started %d 条", id, started[id])
		}
	}
	if n := b2.slots.running(); n != 0 {
		t.Fatalf("中断的任务不该占并发槽，实际 %d", n)
	}
}

// 取消外部任务时走 Stop（TERM→5s→KILL 由 cli 实现），并且不误触发 SIGSTOP。
func TestCancelExternalTaskUsesStop(t *testing.T) {
	b, _ := externalBot(t, "omp", cli.Options{})
	b.cfg.StallTimeout = time.Minute
	h := &stubHandle{done: make(chan exec.Result, 1)}
	b.execs = exec.NewRegistry(stubRunner{kind: exec.KindOMP, h: h})
	// Close 会等 startExternal 的收尾 goroutine；测试中途失败时也要给它一个结果。
	t.Cleanup(func() {
		select {
		case h.done <- exec.Result{Status: "cancelled"}:
		default:
		}
	})
	id, err := b.CreateTask("外部任务被停止", "", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "external task running", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "running"
	})
	if err := b.CancelTask(id, "user"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stopped", func() bool { p, s := h.counts(); return p == 0 && s == 1 })
	h.done <- exec.Result{Status: "cancelled"}
	waitFor(t, "cancelled", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "cancelled" && rec.StopReason == "user"
	})
}

// startWatchStalls 只启动监工循环。externalBot 没有聊天模型，走完整的 Start() 会让
// chatLoop 调到 nil 的 fakeAI；卡死检测只需要监工在跑。
func startWatchStalls(b *Bot) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.watchStalls()
	}()
}

// 心跳只证明进程活着：持续心跳但没有任何输出，超过 ExternalStallTimeout 仍然算卡死。
func TestExternalStallIgnoresHeartbeats(t *testing.T) {
	b, _ := externalBot(t, "omp", cli.Options{})
	b.cfg.StallTimeout = time.Second
	b.cfg.ExternalStallTimeout = 250 * time.Millisecond
	h := &stubHandle{done: make(chan exec.Result, 1)}
	b.execs = exec.NewRegistry(stubRunner{kind: exec.KindOMP, h: h})
	t.Cleanup(func() {
		select {
		case h.done <- exec.Result{Status: "cancelled"}:
		default:
		}
	})
	startWatchStalls(b)
	id, err := b.CreateTask("有心跳但没输出", "", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "external task running", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "running" && !rec.LastProgress.IsZero()
	})
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		tk := time.NewTicker(30 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				b.emitExecEvent(id, exec.Event{Kind: exec.EventHeartbeat})
			}
		}
	}()
	waitFor(t, "paused after no output", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "paused" && rec.StopReason == "stall"
	})
	if p, _ := h.counts(); p != 1 {
		t.Fatalf("应挂起一次进程，实际 pause=%d", p)
	}
}

// 外部执行器不一定会自己实现超时：监工按 MaxDuration 兜底 Stop，并把任务落成 failed。
func TestExternalTaskTimeoutStopsProcess(t *testing.T) {
	b, _ := externalBot(t, "omp", cli.Options{})
	b.cfg.TaskMaxTime = 150 * time.Millisecond
	b.cfg.StallTimeout = time.Minute
	b.cfg.ExternalStallTimeout = time.Minute
	h := &autoStopHandle{stubHandle: &stubHandle{done: make(chan exec.Result, 1)}}
	b.execs = exec.NewRegistry(handleRunner{kind: exec.KindOMP, h: h})
	id, err := b.CreateTask("跑很久的任务", "", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "failed by timeout", func() bool {
		rec, ok := b.store.Task(id)
		return ok && terminal(rec.Status)
	})
	rec, _ := b.store.Task(id)
	if rec.Status != "failed" || !strings.Contains(rec.Error, "没有完成，已终止") {
		t.Fatalf("超时应落 failed 并说明原因：%+v", rec)
	}
	if _, s := h.counts(); s != 1 {
		t.Fatalf("超时应 Stop 进程一次，实际 %d", s)
	}
}

// 平台不支持挂起（Windows）+ 监工卡死：挂不住就直接终止，落 failed 并说明原因。
// 绝不能留下“记录说暂停、进程却还在跑”的状态。
func TestStallUnsupportedTerminatesProcess(t *testing.T) {
	b, _ := externalBot(t, "omp", cli.Options{})
	b.cfg.ExternalStallTimeout = 100 * time.Millisecond
	h := &pauseErrHandle{stubHandle: &stubHandle{done: make(chan exec.Result, 1)}, err: cli.ErrSignalUnsupported}
	h.autoDone = true // Stop 之后进程退出
	b.execs = exec.NewRegistry(handleRunner{kind: exec.KindOMP, h: h})
	id, err := b.CreateTask("不支持挂起的平台", "", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "external task running", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "running" && !rec.LastProgress.IsZero()
	})
	b.stallExternal(id)
	waitFor(t, "failed after unsupported stall", func() bool {
		rec, ok := b.store.Task(id)
		return ok && terminal(rec.Status)
	})
	rec, _ := b.store.Task(id)
	if rec.Status != "failed" || !strings.Contains(rec.Error, "不支持挂起，已终止") {
		t.Fatalf("不支持挂起时应终止并落 failed：%+v", rec)
	}
	if _, s := h.counts(); s != 1 {
		t.Fatalf("应 Stop 进程一次，实际 %d", s)
	}
}

// 用户主动暂停遇到不支持挂起的平台：不改状态，返回明确错误。
func TestPauseUnsupportedReturnsError(t *testing.T) {
	b, _ := externalBot(t, "omp", cli.Options{})
	h := &pauseErrHandle{stubHandle: &stubHandle{done: make(chan exec.Result, 1)}, err: cli.ErrSignalUnsupported}
	b.execs = exec.NewRegistry(handleRunner{kind: exec.KindOMP, h: h})
	t.Cleanup(func() {
		select {
		case h.done <- exec.Result{Status: "cancelled"}:
		default:
		}
	})
	id, err := b.CreateTask("暂停会失败", "", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "external task running", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "running" && !rec.LastProgress.IsZero()
	})
	if _, err := b.PauseTask(id, "user"); err == nil || !strings.Contains(err.Error(), "不支持挂起，请用停止代替") {
		t.Fatalf("应返回不支持挂起的明确错误，得到 %v", err)
	}
	rec, _ := b.store.Task(id)
	if rec.Status != "running" || rec.Pausing || rec.StopReason != "" {
		t.Fatalf("暂停失败后不该改状态：%+v", rec)
	}
}

// 挂起失败（非平台限制）：清掉 pausing、恢复 running，并说明该执行器不支持挂起。
func TestStallPauseFailureResumesRunning(t *testing.T) {
	b, _ := externalBot(t, "omp", cli.Options{})
	b.cfg.ExternalStallTimeout = 100 * time.Millisecond
	h := &pauseErrHandle{stubHandle: &stubHandle{done: make(chan exec.Result, 1)}, err: errors.New("kill: operation not permitted")}
	b.execs = exec.NewRegistry(handleRunner{kind: exec.KindOMP, h: h})
	t.Cleanup(func() {
		select {
		case h.done <- exec.Result{Status: "cancelled"}:
		default:
		}
	})
	id, err := b.CreateTask("挂起失败的任务", "", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "external task running", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "running" && !rec.LastProgress.IsZero()
	})
	b.stallExternal(id)
	rec, _ := b.store.Task(id)
	if rec.Status != "running" || rec.Pausing {
		t.Fatalf("挂起失败后任务应继续运行：%+v", rec)
	}
	if note := strings.Join(rec.Notes, "\n"); !strings.Contains(note, "不支持挂起，任务继续运行") {
		t.Fatalf("应说明挂起失败并继续运行：%q", note)
	}
}

// handleRunner 直接返回给定 Handle，用来注入会失败/会自动结束的假进程。
type handleRunner struct {
	kind exec.Kind
	h    exec.Handle
}

func (r handleRunner) Kind() exec.Kind                 { return r.kind }
func (r handleRunner) Available(context.Context) error { return nil }
func (r handleRunner) Start(context.Context, exec.Request, exec.Sink) (exec.Handle, error) {
	return r.h, nil
}

// autoStopHandle 被 Stop 时立刻给 Done 一个结果，模拟终止后进程退出。
type autoStopHandle struct {
	*stubHandle
	once sync.Once
}

func (h *autoStopHandle) Stop(ctx context.Context) error {
	err := h.stubHandle.Stop(ctx)
	h.once.Do(func() { h.done <- exec.Result{Status: "cancelled"} })
	return err
}

// pauseErrHandle 让 Pause 返回指定错误；autoDone 时 Stop 会立刻让 Done 出一个结果。
type pauseErrHandle struct {
	*stubHandle
	err      error
	autoDone bool
	once     sync.Once
}

func (h *pauseErrHandle) Pause(context.Context) error { return h.err }

func (h *pauseErrHandle) Stop(ctx context.Context) error {
	err := h.stubHandle.Stop(ctx)
	if h.autoDone {
		h.once.Do(func() { h.done <- exec.Result{Status: "cancelled"} })
	}
	return err
}

// 监工侧超时不计暂停时间：暂停时长超过 MaxDuration 也不该被终止，恢复后仍能正常跑完。
func TestPausedTimeExcludedFromTimeout(t *testing.T) {
	b, _ := externalBot(t, "omp", cli.Options{})
	b.cfg.TaskMaxTime = 200 * time.Millisecond
	b.cfg.StallTimeout = time.Minute
	b.cfg.ExternalStallTimeout = time.Minute
	h := &stubHandle{done: make(chan exec.Result, 1)}
	b.execs = exec.NewRegistry(stubRunner{kind: exec.KindOMP, h: h})
	t.Cleanup(func() {
		select {
		case h.done <- exec.Result{Status: "cancelled"}:
		default:
		}
	})
	id, err := b.CreateTask("暂停不计超时", "", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "external task running", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "running" && !rec.LastProgress.IsZero()
	})
	if _, err := b.PauseTask(id, "user"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "paused", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "paused"
	})
	time.Sleep(300 * time.Millisecond) // 超过 TaskMaxTime，但暂停中不该计时
	if rec, _ := b.store.Task(id); rec.Status != "paused" {
		t.Fatalf("暂停期间不该被超时终止：%+v", rec)
	}
	if _, s := h.counts(); s != 0 {
		t.Fatalf("暂停期间不该 Stop 进程，实际 %d", s)
	}
	if _, err := b.ResumeTask(id, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running again", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "running"
	})
	h.done <- exec.Result{Status: "completed", Output: "好了"}
	waitFor(t, "completed", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "completed"
	})
}

// 恢复后的外部任务必须继续参与卡死检测：暂停原因要清掉，否则 watchStalls 会一直跳过它。
func TestStallAfterResumeExternalTask(t *testing.T) {
	b, _ := externalBot(t, "omp", cli.Options{})
	b.cfg.StallTimeout = time.Second
	b.cfg.ExternalStallTimeout = 250 * time.Millisecond
	h := &stubHandle{done: make(chan exec.Result, 1)}
	b.execs = exec.NewRegistry(stubRunner{kind: exec.KindOMP, h: h})
	t.Cleanup(func() {
		select {
		case h.done <- exec.Result{Status: "cancelled"}:
		default:
		}
	})
	startWatchStalls(b)
	id, err := b.CreateTask("暂停恢复后没输出", "", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "external task running", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "running" && !rec.LastProgress.IsZero()
	})
	if _, err := b.PauseTask(id, "user"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "paused", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "paused" && rec.StopReason == "user"
	})
	if _, err := b.ResumeTask(id, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running again without stop reason", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "running" && rec.StopReason == ""
	})
	waitFor(t, "paused by stall after resume", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "paused" && rec.StopReason == "stall"
	})
	var pausing int
	for _, e := range b.store.Since(0) {
		if e.Kind != "task.pausing" {
			continue
		}
		var p TaskRec
		if json.Unmarshal(e.Data, &p) == nil && p.ID == id && p.StopReason == "stall" {
			pausing++
		}
	}
	if pausing != 1 {
		t.Fatalf("恢复后应重新触发一次 task.pausing{stall}，实际 %d", pausing)
	}
}

// ctxStopHandle 模拟 cli.Proc：context 取消（停机）时进程被终止并交出结果。
type ctxStopHandle struct {
	*stubHandle
}

func newCtxStopHandle(ctx context.Context) *ctxStopHandle {
	h := &ctxStopHandle{stubHandle: &stubHandle{done: make(chan exec.Result, 1)}}
	go func() {
		<-ctx.Done()
		select {
		// 与 cli.Proc 一致：被 Stop 打断的运行带 Interrupted 标记。
		case h.done <- exec.Result{Status: "cancelled", Interrupted: true}:
		default:
		}
	}()
	return h
}

// ctxRunner 把启动时的 context 交给句柄，用来观察停机。
type ctxRunner struct {
	kind exec.Kind
	mk   func(context.Context) exec.Handle
}

func (r ctxRunner) Kind() exec.Kind                 { return r.kind }
func (r ctxRunner) Available(context.Context) error { return nil }
func (r ctxRunner) Start(ctx context.Context, _ exec.Request, _ exec.Sink) (exec.Handle, error) {
	return r.mk(ctx), nil
}

// 优雅停机（Close，对应 SIGTERM）：运行中的外部任务落 paused/interrupted，
// 不写 cancelled、不汇报；重启后仍是 interrupted，且不会自动重跑。
func TestCloseMarksExternalInterrupted(t *testing.T) {
	dir := t.TempDir()
	b := newBotWith(t, dir, nil, func(c *Config) {
		c.StallTimeout = time.Minute
		c.ExternalStallTimeout = time.Minute
	})
	b.modelMu.Lock()
	b.executors = Executors{Allowed: []string{string(exec.KindBuiltin), "omp"}, Default: "omp",
		CLIs: map[string]cli.Options{"omp": {}}}
	b.modelMu.Unlock()
	b.execs = exec.NewRegistry(ctxRunner{kind: exec.KindOMP, mk: func(ctx context.Context) exec.Handle {
		return newCtxStopHandle(ctx)
	}})
	id, err := b.CreateTask("停机中的外部任务", "", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "external task running", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "running"
	})
	b.Close() // ctx 取消 -> 句柄交出 cancelled，但记录必须落 interrupted

	b2 := newBotWith(t, dir, nil, nil)
	defer b2.Close()
	rec, ok := b2.store.Task(id)
	if !ok {
		t.Fatal("任务丢失")
	}
	if rec.Status != "paused" || rec.StopReason != "interrupted" {
		t.Fatalf("优雅停机应落 paused/interrupted，得到 %s/%s", rec.Status, rec.StopReason)
	}
	var created, started, cancelled, interruptedUpdates int
	for _, e := range b2.store.Since(0) {
		switch e.Kind {
		case "task.created":
			var r TaskRec
			if json.Unmarshal(e.Data, &r) == nil && r.ID == id {
				created++
			}
		case "task.updated":
			var r TaskRec
			if json.Unmarshal(e.Data, &r) == nil && r.ID == id && r.StopReason == "interrupted" {
				interruptedUpdates++
			}
		case "agent":
			var ev persistedAgent
			if json.Unmarshal(e.Data, &ev) != nil || ev.TaskID != id {
				continue
			}
			switch ev.Type {
			case "task.started":
				started++
			case "task.cancelled":
				cancelled++
			}
		}
	}
	if created != 1 || started != 1 {
		t.Fatalf("停机不该重复派发：created=%d started=%d", created, started)
	}
	if cancelled != 0 {
		t.Fatalf("优雅停机不该写 cancelled，实际 %d 条", cancelled)
	}
	if interruptedUpdates != 1 {
		t.Fatalf("recover 应幂等：interrupted 更新应恰好 1 条，实际 %d", interruptedUpdates)
	}
	for _, text := range noticeTexts(b2) {
		if strings.Contains(text, "外部执行器任务已中断") {
			t.Fatalf("重启后不该重复通知中断：%q", text)
		}
	}
	// 中断 ≠ 重跑：即使重新调度也不会有新的 created/started。
	b2.schedule()
	time.Sleep(200 * time.Millisecond)
	created, started = 0, 0
	for _, e := range b2.store.Since(0) {
		switch e.Kind {
		case "task.created":
			var r TaskRec
			if json.Unmarshal(e.Data, &r) == nil && r.ID == id {
				created++
			}
		case "agent":
			var ev persistedAgent
			if json.Unmarshal(e.Data, &ev) == nil && ev.TaskID == id && ev.Type == "task.started" {
				started++
			}
		}
	}
	if created != 1 || started != 1 {
		t.Fatalf("中断后不该自动重跑：created=%d started=%d", created, started)
	}
	if n := b2.slots.running(); n != 0 {
		t.Fatalf("停机后不应占用并发槽，实际 %d", n)
	}
}

// 用户暂停的外部任务（Status=paused、StopReason=user）遇到优雅停机仍按中断收尾：
// 重启后可以继续完成，而不是被当成取消（那样既不能恢复，用户也收不到说明）。
func TestCloseWhilePausedKeepsInterrupted(t *testing.T) {
	dir := t.TempDir()
	b := newBotWith(t, dir, nil, func(c *Config) {
		c.StallTimeout = time.Minute
		c.ExternalStallTimeout = time.Minute
	})
	b.modelMu.Lock()
	b.executors = Executors{Allowed: []string{string(exec.KindBuiltin), "omp"}, Default: "omp",
		CLIs: map[string]cli.Options{"omp": {}}}
	b.modelMu.Unlock()
	b.execs = exec.NewRegistry(ctxRunner{kind: exec.KindOMP, mk: func(ctx context.Context) exec.Handle {
		return newCtxStopHandle(ctx)
	}})
	id, err := b.CreateTask("暂停中停机", "", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "external task running", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "running"
	})
	if _, err := b.PauseTask(id, "user"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "paused", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "paused" && rec.StopReason == "user" && !rec.Pausing
	})
	b.Close() // 停机打断的是暂停中的进程：必须仍落 interrupted

	b2 := newBotWith(t, dir, nil, nil)
	defer b2.Close()
	rec, ok := b2.store.Task(id)
	if !ok {
		t.Fatal("任务丢失")
	}
	if rec.Status != "paused" || rec.StopReason != "interrupted" {
		t.Fatalf("暂停中的任务停机应为 paused/interrupted，得到 %s/%s", rec.Status, rec.StopReason)
	}
	for _, e := range b2.store.Since(0) {
		if e.Kind != "agent" {
			continue
		}
		var ev persistedAgent
		if json.Unmarshal(e.Data, &ev) == nil && ev.TaskID == id && ev.Type == "task.cancelled" {
			t.Fatal("暂停中停机不该被记成取消")
		}
	}

	// 重启后仍可继续，并正常完成。
	b2.modelMu.Lock()
	b2.executors = Executors{Allowed: []string{string(exec.KindBuiltin), "omp"}, Default: "omp",
		CLIs: map[string]cli.Options{"omp": {}}}
	b2.modelMu.Unlock()
	h := &stubHandle{done: make(chan exec.Result, 1)}
	b2.execs = exec.NewRegistry(handleRunner{kind: exec.KindOMP, h: h})
	if _, err := b2.ResumeTask(id, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "resumed running", func() bool {
		rec, ok := b2.store.Task(id)
		return ok && rec.Status == "running"
	})
	h.done <- exec.Result{Status: "completed", Output: "继续完成"}
	waitFor(t, "completed", func() bool {
		rec, ok := b2.store.Task(id)
		return ok && rec.Status == "completed"
	})
}

// 真正的取消在途（运行中、StopReason=user、没有请求暂停）遇到停机仍由取消路径收尾：
// 写 cancelled/user，而不是可恢复的 interrupted。
func TestShutdownDuringCancelKeepsCancelled(t *testing.T) {
	b, _ := externalBot(t, "omp", cli.Options{})
	h := &stubHandle{done: make(chan exec.Result, 1)}
	b.execs = exec.NewRegistry(stubRunner{kind: exec.KindOMP, h: h})
	id, err := b.CreateTask("取消在途", "", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "running"
	})
	if err := b.CancelTask(id, "user"); err != nil {
		t.Fatal(err)
	}
	if rec, _ := b.store.Task(id); rec.Status != "running" || rec.StopReason != "user" || rec.Pausing {
		t.Fatalf("前置条件失败：取消应还在途：%+v", rec)
	}
	b.shuttingDown.Store(true)
	h.done <- exec.Result{Status: "cancelled", Interrupted: true}
	waitFor(t, "cancelled", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "cancelled" && rec.StopReason == "user"
	})
	b.shuttingDown.Store(false)
}

// 内置任务保持原语义：优雅停机按取消收尾（cancelled），不标 interrupted。
func TestCloseCancelsBuiltinTask(t *testing.T) {
	dir := t.TempDir()
	ai := &fakeAI{agent: blockingAgent}
	b := newBot(t, dir, ai)
	b.Start()
	id, err := b.StartTask("内置停机", "内置停机")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "builtin task running", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "running"
	})
	b.Close()

	b2 := newBot(t, dir, ai)
	defer b2.Close()
	rec, ok := b2.store.Task(id)
	if !ok {
		t.Fatal("任务丢失")
	}
	if rec.Status != "cancelled" {
		t.Fatalf("内置任务停机仍应按取消收尾，得到 %s/%s", rec.Status, rec.StopReason)
	}
	if rec.StopReason == "interrupted" {
		t.Fatalf("内置任务不该标 interrupted：%+v", rec)
	}
}

// newCtxResultHandle 在 context 取消时交出一个指定结果，模拟“CLI 恰好在停机窗口内结束”。
func newCtxResultHandle(ctx context.Context, res exec.Result) *stubHandle {
	h := &stubHandle{done: make(chan exec.Result, 1)}
	go func() {
		<-ctx.Done()
		select {
		case h.done <- res:
		default:
		}
	}()
	return h
}

// 停机窗口内 CLI 自然完成：结果按 completed 收尾，输出保留，不能被改写成 interrupted。
func TestCloseKeepsNaturalCompletion(t *testing.T) {
	dir := t.TempDir()
	b := newBotWith(t, dir, nil, func(c *Config) {
		c.StallTimeout = time.Minute
		c.ExternalStallTimeout = time.Minute
	})
	b.modelMu.Lock()
	b.executors = Executors{Allowed: []string{string(exec.KindBuiltin), "omp"}, Default: "omp",
		CLIs: map[string]cli.Options{"omp": {}}}
	b.modelMu.Unlock()
	b.execs = exec.NewRegistry(ctxRunner{kind: exec.KindOMP, mk: func(ctx context.Context) exec.Handle {
		return newCtxResultHandle(ctx, exec.Result{Status: "completed", Output: "收尾输出"})
	}})
	id, err := b.CreateTask("停机前刚好完成", "", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "external task running", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "running"
	})
	b.Close()

	b2 := newBotWith(t, dir, nil, nil)
	defer b2.Close()
	rec, ok := b2.store.Task(id)
	if !ok {
		t.Fatal("任务丢失")
	}
	if rec.Status != "completed" || rec.Output != "收尾输出" {
		t.Fatalf("停机窗口内的自然完成应按 completed 收尾并保留输出：%+v", rec)
	}
}

// 停机窗口内用户取消仍在路上：让取消路径正常写 cancelled，不能停在 running 或被改写。
func TestCloseKeepsUserCancel(t *testing.T) {
	dir := t.TempDir()
	b := newBotWith(t, dir, nil, func(c *Config) {
		c.StallTimeout = time.Minute
		c.ExternalStallTimeout = time.Minute
	})
	b.modelMu.Lock()
	b.executors = Executors{Allowed: []string{string(exec.KindBuiltin), "omp"}, Default: "omp",
		CLIs: map[string]cli.Options{"omp": {}}}
	b.modelMu.Unlock()
	h := &stubHandle{done: make(chan exec.Result, 1)}
	go func() {
		<-b.ctx.Done() // 停机时进程才真正退出
		h.done <- exec.Result{Status: "cancelled"}
	}()
	b.execs = exec.NewRegistry(handleRunner{kind: exec.KindOMP, h: h})
	id, err := b.CreateTask("取消进行中被停机", "", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "external task running", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "running"
	})
	if err := b.CancelTask(id, "user"); err != nil {
		t.Fatal(err)
	}
	b.Close()

	b2 := newBotWith(t, dir, nil, nil)
	defer b2.Close()
	rec, ok := b2.store.Task(id)
	if !ok {
		t.Fatal("任务丢失")
	}
	if rec.Status != "cancelled" || rec.StopReason != "user" {
		t.Fatalf("用户取消应按 cancelled/user 收尾：%s/%s", rec.Status, rec.StopReason)
	}
}

// 删除 Bot（stop() 同样用于 DELETE /api/bots/{id}）时，正在跑的外部任务必须归还共享并发槽。
func TestCloseBotReleasesSharedSlot(t *testing.T) {
	dir := t.TempDir()
	owner := newBotWith(t, dir, nil, nil)
	defer owner.Close()
	cfg := Config{DataDir: filepath.Join(dir, "data2"), Workspace: filepath.Join(dir, "ws2"),
		StallTimeout: time.Minute, ExternalStallTimeout: time.Minute}
	b, err := newRuntime(cfg, owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.slots != owner.slots {
		t.Fatal("第二个 Bot 应共享同一个 taskSlots")
	}
	b.modelMu.Lock()
	b.executors = Executors{Allowed: []string{string(exec.KindBuiltin), "omp"}, Default: "omp",
		CLIs: map[string]cli.Options{"omp": {}}}
	b.modelMu.Unlock()
	b.execs = exec.NewRegistry(ctxRunner{kind: exec.KindOMP, mk: func(ctx context.Context) exec.Handle {
		return newCtxStopHandle(ctx)
	}})
	id, err := b.CreateTask("删 Bot 时的外部任务", "", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "external task running", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "running"
	})
	if n := owner.slots.running(); n != 1 {
		t.Fatalf("运行中应占 1 个共享槽，实际 %d", n)
	}
	b.Close()
	if n := owner.slots.running(); n != 0 {
		t.Fatalf("删除 Bot 后应归还共享槽，实际 %d", n)
	}
}

// 停机中断的定时任务：claim 必须保持 dispatched（等同普通暂停），
// 否则调度器的 OpenClaimsFor/recoverLocked 认不出未完成运行，完成时也写不进 FinishedAt。
//
// S1 之后定时计划固定派生内置任务，所以这里直接用「S1 之前的日志形状」构造一条
// 由计划派生、执行器为 omp 的任务记录：升级后这类历史数据仍要按原语义恢复。
func TestShutdownInterruptKeepsScheduleClaimDispatched(t *testing.T) {
	loc, err := time.LoadLocation(scheduler.DefaultTimezone)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	b := newBotWith(t, dir, nil, func(c *Config) {
		c.StallTimeout = time.Minute
		c.ExternalStallTimeout = time.Minute
	})
	plan, err := b.sched.Upsert(scheduler.Draft{Title: "停机计划", Prompt: "跑一次", Enabled: true,
		Overlap: scheduler.OverlapSkip,
		Spec:    scheduler.Spec{Kind: scheduler.Daily, Timezone: scheduler.DefaultTimezone, Hour: 9, Minute: 0}})
	if err != nil {
		t.Fatal(err)
	}
	due := time.Date(2026, 11, 9, 9, 0, 0, 0, loc)
	key := scheduler.OccurrenceKey(plan.ID, due)
	id := scheduler.TaskIDFor(key)
	if _, _, err := b.sched.Store.TryClaim(scheduler.Claim{Key: key, PlanID: plan.ID, TaskID: id,
		IntendedAt: due, ClaimedAt: time.Now().UTC(), Status: "claimed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.sched.Store.UpdateClaim(key, "dispatched", "test"); err != nil {
		t.Fatal(err)
	}
	b.store.Append("task.created", TaskRec{ID: id, Title: "停机计划", Input: "跑一次", Executor: "omp",
		Status: "running", CreatedAt: time.Now()}, false)
	b.shuttingDown.Store(true)
	if !b.markInterruptedOnShutdown(id) {
		t.Fatal("停机应把运行中的外部任务判为中断")
	}
	b.shuttingDown.Store(false)
	b.Close() // 停机收尾

	// 重启：claim 仍是 dispatched，任务 paused/interrupted
	b2 := newBotWith(t, dir, nil, nil)
	defer b2.Close()
	b2.modelMu.Lock()
	b2.executors = Executors{Allowed: []string{string(exec.KindBuiltin), "omp"}, Default: "omp",
		CLIs: map[string]cli.Options{"omp": {}}}
	b2.modelMu.Unlock()
	fc2 := &fixedClock{t: time.Date(2026, 11, 9, 9, 0, 2, 0, loc)}
	b2.sched.Clock = fc2
	c, ok := b2.sched.Store.Claim(key)
	if !ok {
		t.Fatal("claim 丢失")
	}
	if c.Status != "dispatched" || c.FinishedAt != nil {
		t.Fatalf("停机中断不该改 claim 状态：%+v", c)
	}
	open := b2.sched.Store.OpenClaimsFor(plan.ID)
	found := false
	for _, oc := range open {
		if oc.Key == key {
			found = true
		}
	}
	if !found {
		t.Fatalf("OpenClaimsFor 应仍能看到这条未完成运行：%+v", open)
	}
	if rec, _ := b2.store.Task(id); rec.Status != "paused" || rec.StopReason != "interrupted" {
		t.Fatalf("任务应为 paused/interrupted：%+v", rec)
	}

	// overlap=skip：上一趟还没完成，下一趟不能并发派新任务
	next := time.Date(2026, 11, 10, 9, 0, 0, 0, loc)
	if err := b2.sched.Store.SetNext(plan.ID, &next); err != nil {
		t.Fatal(err)
	}
	fc2.t = time.Date(2026, 11, 10, 9, 0, 5, 0, loc)
	b2.sched.Tick()
	if n := len(b2.store.Tasks()); n != 1 {
		t.Fatalf("overlap=skip 不该并发派新任务，实际 %d 个", n)
	}
	skipped, ok := b2.sched.Store.Claim(scheduler.OccurrenceKey(plan.ID, next))
	if !ok || skipped.Status != "skipped_overlap" {
		t.Fatalf("这一趟应记为 skipped_overlap：%+v", skipped)
	}

	// 恢复并正常完成：claim 变 completed，FinishedAt 写入
	h := &stubHandle{done: make(chan exec.Result, 1)}
	b2.execs = exec.NewRegistry(handleRunner{kind: exec.KindOMP, h: h})
	t.Cleanup(func() {
		select {
		case h.done <- exec.Result{Status: "cancelled"}:
		default:
		}
	})
	if _, err := b2.ResumeTask(id, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "resumed running", func() bool {
		rec, ok := b2.store.Task(id)
		return ok && rec.Status == "running"
	})
	h.done <- exec.Result{Status: "completed", Output: "做完了"}
	waitFor(t, "completed", func() bool {
		rec, ok := b2.store.Task(id)
		return ok && rec.Status == "completed"
	})
	done, ok := b2.sched.Store.Claim(key)
	if !ok || done.Status != "completed" || done.FinishedAt == nil {
		t.Fatalf("完成后 claim 应为 completed 且写 FinishedAt：%+v", done)
	}
}

// 定时计划派生的外部任务中断：通知要建议恢复/取消，而不是“重新派一次”——
// 重新派一次会留下未闭合的 claim，overlap=skip 的计划会一直跳过。
func TestRecoverScheduledInterruptNotice(t *testing.T) {
	loc, err := time.LoadLocation(scheduler.DefaultTimezone)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	b1 := newBotWith(t, dir, nil, nil)
	plan, err := b1.sched.Upsert(scheduler.Draft{Title: "每日计划", Prompt: "跑", Enabled: true,
		Spec: scheduler.Spec{Kind: scheduler.Daily, Timezone: scheduler.DefaultTimezone, Hour: 9, Minute: 0}})
	if err != nil {
		t.Fatal(err)
	}
	due := time.Date(2026, 11, 9, 9, 0, 0, 0, loc)
	key := scheduler.OccurrenceKey(plan.ID, due)
	scheduledID := scheduler.TaskIDFor(key)
	claim := scheduler.Claim{Key: key, PlanID: plan.ID, TaskID: scheduledID, IntendedAt: due,
		ClaimedAt: time.Now().UTC(), Status: "dispatched"}
	if _, _, err := b1.sched.Store.TryClaim(claim); err != nil {
		t.Fatal(err)
	}
	b1.store.Append("task.created", TaskRec{ID: scheduledID, Title: "定时任务", Input: "跑", Executor: "omp",
		Status: "running", CreatedAt: time.Now()}, false)
	plainID := "task_plainsmoke"
	b1.store.Append("task.created", TaskRec{ID: plainID, Title: "普通任务", Input: "跑", Executor: "omp",
		Status: "running", CreatedAt: time.Now()}, false)
	b1.Close()

	b2 := newBotWith(t, dir, nil, nil)
	defer b2.Close()
	if rec, _ := b2.store.Task(scheduledID); rec.Status != "paused" || rec.StopReason != "interrupted" {
		t.Fatalf("定时任务应为 paused/interrupted：%+v", rec)
	}
	if c, ok := b2.sched.Store.Claim(key); !ok || c.Status != "dispatched" {
		t.Fatalf("中断不该闭合 claim：%+v", c)
	}
	var schedNote, plainNote string
	for _, text := range noticeTexts(b2) {
		if strings.Contains(text, scheduledID) {
			schedNote = text
		}
		if strings.Contains(text, plainID) {
			plainNote = text
		}
	}
	for _, want := range []string{"恢复", "取消", "定时计划"} {
		if !strings.Contains(schedNote, want) {
			t.Fatalf("定时任务的中断通知应包含 %q，实际 %q", want, schedNote)
		}
	}
	if strings.Contains(schedNote, "重新派一次") {
		t.Fatalf("定时任务不该建议重新派一次：%q", schedNote)
	}
	if !strings.Contains(plainNote, "重新派一次") {
		t.Fatalf("普通外部任务的通知应保留「重新派一次」：%q", plainNote)
	}
}

// Done 后的停机判定用 res.Interrupted：只有它 + 正在停机才落 paused/interrupted。
func TestInterruptedResultOnlyOnShutdown(t *testing.T) {
	b, _ := externalBot(t, "omp", cli.Options{})
	h := &stubHandle{done: make(chan exec.Result, 1)}
	b.execs = exec.NewRegistry(stubRunner{kind: exec.KindOMP, h: h})

	// 停机：res.Interrupted=true → paused/interrupted，不写 cancelled。
	id, err := b.CreateTask("停机中断", "", TaskOpts{Executor: "omp"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "running"
	})
	b.shuttingDown.Store(true)
	h.done <- exec.Result{Status: "cancelled", Interrupted: true}
	waitFor(t, "interrupted paused", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "paused" && rec.StopReason == "interrupted"
	})
	b.shuttingDown.Store(false)

	// 非停机：同样带 Interrupted 的取消结果按真实结果落 cancelled。
	h2 := &stubHandle{done: make(chan exec.Result, 1)}
	b.execs = exec.NewRegistry(stubRunner{kind: exec.KindOMP, h: h2})
	id2, err := b.CreateTask("普通取消", "", TaskOpts{Executor: "omp"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running again", func() bool {
		rec, ok := b.store.Task(id2)
		return ok && rec.Status == "running"
	})
	b.store.Append("task.updated", TaskRec{ID: id2, StopReason: "user"}, false)
	h2.done <- exec.Result{Status: "cancelled", Interrupted: true}
	waitFor(t, "cancelled", func() bool {
		rec, ok := b.store.Task(id2)
		return ok && rec.Status == "cancelled"
	})
}
