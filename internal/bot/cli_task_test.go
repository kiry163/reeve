//go:build !windows

package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiry163/reeve/internal/exec"
	"github.com/kiry163/reeve/internal/exec/cli"
)

func writeFakeCLI(t *testing.T, dir, name, script string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// externalBot 建一个 Bot，并把 fake CLI 所在目录放在 PATH 最前面。
func externalBot(t *testing.T, cliName string, opts cli.Options) (*Bot, string) {
	t.Helper()
	bin := t.TempDir()
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("HOME", t.TempDir()) // 不要看到本机真实安装的 CLI
	t.Setenv("NVM_DIR", "")
	b := newBotWith(t, t.TempDir(), nil, func(c *Config) { c.StallTimeout = time.Minute })
	t.Cleanup(b.Close)
	b.modelMu.Lock()
	b.executors = Executors{Allowed: []string{string(exec.KindBuiltin), cliName}, Default: cliName, CLIs: map[string]cli.Options{cliName: opts}}
	b.modelMu.Unlock()
	return b, bin
}

func TestExternalTaskStreamsAndCompletes(t *testing.T) {
	b, bin := externalBot(t, "omp", cli.Options{})
	writeFakeCLI(t, bin, "omp", `#!/bin/sh
echo '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"外部"}}'
echo '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"执行完成"}}'
`)
	events, unsubscribe := b.store.Subscribe()
	defer unsubscribe()
	id, err := b.CreateTask("跑一下", "", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "external task completed", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "completed"
	})
	rec, _ := b.store.Task(id)
	if rec.Output != "外部执行完成" {
		t.Fatalf("输出不对：%q", rec.Output)
	}
	// 流式文字只通过 agent 事件推送，不落盘。
	var textEvents int
drain:
	for {
		select {
		case e := <-events:
			if e.Kind != "agent" {
				continue
			}
			var payload struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(e.Data, &payload) == nil && payload.Type == "text.delta" {
				textEvents++
			}
		default:
			break drain
		}
	}
	if textEvents == 0 {
		t.Fatal("流式文字应通过 agent 事件推送")
	}
	for _, e := range b.store.Since(0) {
		if e.Kind != "agent" {
			continue
		}
		var payload struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(e.Data, &payload) == nil && payload.Type == "text.delta" {
			t.Fatal("流式文字不应写进事件日志")
		}
	}
}

func TestExternalTaskFailureReportsError(t *testing.T) {
	b, bin := externalBot(t, "omp", cli.Options{})
	writeFakeCLI(t, bin, "omp", "#!/bin/sh\necho '{\"type\":\"error\",\"error\":{\"message\":\"boom\"}}'\n")
	id, err := b.CreateTask("会失败", "", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "external task failed", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "failed"
	})
	rec, _ := b.store.Task(id)
	if !strings.Contains(rec.Error, "boom") {
		t.Fatalf("失败原因不对：%+v", rec)
	}
}

func TestCancelExternalTaskReleasesSlot(t *testing.T) {
	b, bin := externalBot(t, "omp", cli.Options{})
	writeFakeCLI(t, bin, "omp", "#!/bin/sh\nwhile true; do sleep 0.1; done\n")
	before := b.slots.running()
	id, err := b.CreateTask("一直跑", "", TaskOpts{})
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
	waitFor(t, "external task cancelled", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "cancelled"
	})
	waitFor(t, "slot released", func() bool { return b.slots.running() == before })
	rec, _ := b.store.Task(id)
	if rec.StopReason != "user" {
		t.Fatalf("取消原因不对：%+v", rec)
	}
}

type countingGateway struct {
	issued  atomic.Int32
	revoked atomic.Int32
}

func (g *countingGateway) Issue(string, string, time.Duration) (string, string, func(), error) {
	g.issued.Add(1)
	return "http://127.0.0.1:9/v1", "gw-token", func() { g.revoked.Add(1) }, nil
}

func TestPiChannelEnvCleanupRunsAfterProcess(t *testing.T) {
	b, bin := externalBot(t, "pi", cli.Options{Auth: cli.AuthChannel, Channel: "relay", Model: "m"})
	writeFakeCLI(t, bin, "pi", "#!/bin/sh\necho done\n")
	gw := &countingGateway{}
	cli.SetGateway(gw)
	t.Cleanup(func() { cli.SetGateway(nil) })
	id, err := b.CreateTask("用 pi 跑", "", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "pi task completed", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "completed"
	})
	if gw.issued.Load() != 1 || gw.revoked.Load() != 1 {
		t.Fatalf("ChannelEnv cleanup 未执行：issued=%d revoked=%d", gw.issued.Load(), gw.revoked.Load())
	}
}

type persistedAgent struct {
	TaskID string          `json:"task_id"`
	Type   string          `json:"type"`
	Data   json.RawMessage `json:"data"`
}

func agentEvents(b *Bot) []persistedAgent {
	var out []persistedAgent
	for _, e := range b.store.Since(0) {
		if e.Kind != "agent" {
			continue
		}
		var ev persistedAgent
		if json.Unmarshal(e.Data, &ev) == nil {
			out = append(out, ev)
		}
	}
	return out
}

func noticeTexts(b *Bot) []string {
	b.store.mu.Lock()
	defer b.store.mu.Unlock()
	var out []string
	for _, e := range b.store.log {
		if e.Kind != "message" {
			continue
		}
		var m Message
		if json.Unmarshal(e.Data, &m) == nil && m.Role == "notice" {
			out = append(out, m.Text)
		}
	}
	return out
}

// 外部任务产生工具事件后：Steps 与 StepLog/汇报 summary 一致。
func TestExternalToolEventsPersistAndSteps(t *testing.T) {
	b, bin := externalBot(t, "omp", cli.Options{})
	writeFakeCLI(t, bin, "omp", `#!/bin/sh
echo '{"type":"message_update","assistantMessageEvent":{"type":"toolcall_end","contentIndex":1,"toolCall":{"id":"call_1","name":"bash","arguments":{"command":"echo hi"},"partialArgs":"{\"command\":\"echo hi\"}"}}}'
echo '{"type":"tool_execution_end","toolCallId":"call_1","toolName":"bash","isError":false,"result":{"content":[{"type":"text","text":"ok"}]}}'
echo '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"done"}}'
`)
	id, err := b.CreateTask("跑一个工具", "", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "task summary", func() bool {
		for _, e := range b.store.Since(0) {
			if e.Kind == "task.summary" && strings.Contains(string(e.Data), id) {
				return true
			}
		}
		return false
	})
	rec, _ := b.store.Task(id)
	if rec.Steps == 0 || rec.Steps != len(rec.StepLog) {
		t.Fatalf("Steps 应为工具调用数：steps=%d steplog=%d", rec.Steps, len(rec.StepLog))
	}
	var summarySteps int
	for _, e := range b.store.Since(0) {
		if e.Kind != "task.summary" {
			continue
		}
		var sm TaskSummary
		if json.Unmarshal(e.Data, &sm) == nil && sm.ID == id {
			summarySteps = sm.Steps
		}
	}
	if summarySteps != rec.Steps {
		t.Fatalf("汇报 summary 步数 %d 与任务步数 %d 不一致", summarySteps, rec.Steps)
	}
	foundNotice := false
	waitFor(t, "task summary notice", func() bool {
		for _, text := range noticeTexts(b) {
			if strings.Contains(text, fmt.Sprintf("共 %d 步", rec.Steps)) {
				foundNotice = true
				return true
			}
		}
		return false
	})
	if !foundNotice {
		t.Fatalf("汇报文案里的步数与 summary 不一致（steps=%d）：%q", rec.Steps, noticeTexts(b))
	}
	var calls, results int
	for _, ev := range agentEvents(b) {
		switch ev.Type {
		case "tool.call":
			calls++
			var data struct {
				Tool string          `json:"tool"`
				Args json.RawMessage `json:"args"`
			}
			_ = json.Unmarshal(ev.Data, &data)
			var args map[string]any
			if data.Tool != "bash" || json.Unmarshal(data.Args, &args) != nil || args["command"] != "echo hi" {
				t.Fatalf("工具调用事件不对：%s", ev.Data)
			}
		case "tool.result":
			results++
		}
	}
	if calls != 1 || results != 1 {
		t.Fatalf("工具事件未落盘：call=%d result=%d", calls, results)
	}
}

// emitExecEvent：非法 JSON 参数有兜底，工具输出截断到 16000。
func TestEmitExecEventToolArgsFallbackAndTruncation(t *testing.T) {
	b, _ := externalBot(t, "omp", cli.Options{})
	id := "task_emit"
	b.store.Append("task.created", TaskRec{ID: id, Title: "t", Input: "x", Status: "running", CreatedAt: time.Now()}, false)
	b.emitExecEvent(id, exec.Event{Kind: exec.EventTool, Tool: "mystery", Input: "not-json"})
	b.emitExecEvent(id, exec.Event{Kind: exec.EventToolResult, Tool: "mystery", Output: strings.Repeat("x", 20000)})
	var call, result string
	for _, ev := range agentEvents(b) {
		if ev.TaskID != id {
			continue
		}
		if ev.Type == "tool.call" {
			call = string(ev.Data)
		}
		if ev.Type == "tool.result" {
			result = string(ev.Data)
		}
	}
	if !strings.Contains(call, `"input":"not-json"`) {
		t.Fatalf("非法 JSON 参数没有兜底：%s", call)
	}
	var out struct {
		Output string `json:"output"`
	}
	if json.Unmarshal([]byte(result), &out) != nil || len(out.Output) == 0 || len(out.Output) > 16200 || !strings.Contains(out.Output, "省略") {
		t.Fatalf("工具输出未截断到 16000：len=%d", len(out.Output))
	}
}

// 外部任务暂停后恢复：补充要求无法转达（不落 task.note），且补发 task.started 不覆盖首次 StartedAt。
func TestPauseResumeExternalRecordsNoteAndStarted(t *testing.T) {
	b, bin := externalBot(t, "omp", cli.Options{})
	writeFakeCLI(t, bin, "omp", `#!/bin/sh
echo '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"开始"}}'
while true; do sleep 0.1; done
`)
	id, err := b.CreateTask("先跑再暂停", "", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "running" && !rec.StartedAt.IsZero()
	})
	rec, _ := b.store.Task(id)
	firstStart := rec.StartedAt
	if _, err := b.PauseTask(id, "user"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "paused", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "paused"
	})
	text, err := b.ResumeTask(id, "改成中文")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "无法插话") || !strings.Contains(text, "没有转达") {
		t.Fatalf("恢复文案应如实说明补充要求无法转达：%q", text)
	}
	waitFor(t, "running again", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "running"
	})
	rec, _ = b.store.Task(id)
	if !rec.StartedAt.Equal(firstStart) {
		t.Fatalf("恢复不该覆盖首次开始时间：%v -> %v", firstStart, rec.StartedAt)
	}
	for _, n := range rec.Notes {
		if strings.Contains(n, "改成中文") {
			t.Fatalf("外部执行器无法转达补充，不应假装记下：%+v", rec.Notes)
		}
	}
	var started int
	for _, ev := range agentEvents(b) {
		if ev.TaskID == id && ev.Type == "task.started" {
			started++
		}
	}
	if started < 2 {
		t.Fatalf("恢复时应补发 task.started，实际 %d 条", started)
	}
	if err := b.CancelTask(id, "user"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "cancelled", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "cancelled"
	})
}

// 外部任务暂停事件要带当前步数，暂停通知与最终 summary 一致。
func TestExternalPauseEventCarriesSteps(t *testing.T) {
	b, bin := externalBot(t, "omp", cli.Options{})
	writeFakeCLI(t, bin, "omp", `#!/bin/sh
echo '{"type":"message_update","assistantMessageEvent":{"type":"toolcall_end","contentIndex":1,"toolCall":{"id":"call_1","name":"bash","arguments":{"command":"echo hi"}}}}'
echo '{"type":"tool_execution_end","toolCallId":"call_1","toolName":"bash","isError":false,"result":{"content":[{"type":"text","text":"ok"}]}}'
while true; do sleep 0.1; done
`)
	id, err := b.CreateTask("跑工具后暂停", "", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "tool recorded", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "running" && len(rec.StepLog) > 0
	})
	if _, err := b.PauseTask(id, "chat"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "paused", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "paused"
	})
	rec, _ := b.store.Task(id)
	if rec.Steps == 0 || rec.Steps != len(rec.StepLog) {
		t.Fatalf("暂停事件应带步数：steps=%d steplog=%d", rec.Steps, len(rec.StepLog))
	}
	waitFor(t, "pause notice", func() bool {
		for _, text := range noticeTexts(b) {
			if strings.Contains(text, fmt.Sprintf("第 %d 步", rec.Steps)) {
				return true
			}
		}
		return false
	})
	// 恢复后再取消，产生 summary，确认暂停时的步数与最终 summary 一致。
	if _, err := b.ResumeTask(id, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running again", func() bool {
		cur, ok := b.store.Task(id)
		return ok && cur.Status == "running"
	})
	if err := b.CancelTask(id, "user"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "summary", func() bool {
		for _, e := range b.store.Since(0) {
			if e.Kind == "task.summary" && strings.Contains(string(e.Data), id) {
				return true
			}
		}
		return false
	})
	var summarySteps int
	for _, e := range b.store.Since(0) {
		if e.Kind != "task.summary" {
			continue
		}
		var sm TaskSummary
		if json.Unmarshal(e.Data, &sm) == nil && sm.ID == id {
			summarySteps = sm.Steps
		}
	}
	if summarySteps != rec.Steps {
		t.Fatalf("暂停步数 %d 与 summary 步数 %d 不一致", rec.Steps, summarySteps)
	}
}

// 排队时被暂停、随后才被调度的任务，startExternal 必须真的发出 SIGSTOP（用可观测的假 Handle 断言）。
func TestPauseQueuedThenScheduleActuallyPauses(t *testing.T) {
	b, _ := externalBot(t, "omp", cli.Options{})
	h := &stubHandle{done: make(chan exec.Result, 1)}
	b.execs = exec.NewRegistry(stubRunner{kind: exec.KindOMP, h: h})
	id := "task_paused_start"
	b.store.Append("task.created", TaskRec{ID: id, Title: "t", Input: "x", Executor: "omp",
		Status: "queued", CreatedAt: time.Now()}, false)
	if _, err := b.PauseTask(id, "user"); err != nil {
		t.Fatal(err)
	}
	rec, _ := b.store.Task(id)
	if rec.Status != "paused" {
		t.Fatalf("排队中的任务应被挂起：%+v", rec)
	}
	b.startExternal(rec) // 模拟 schedule 在这之后才把它派发出去
	waitFor(t, "handle registered", func() bool { _, ok := b.cliTask(id); return ok })
	if p, s := h.counts(); p != 1 || s != 0 {
		t.Fatalf("启动窗口内的暂停未真正挂起进程：pause=%d stop=%d", p, s)
	}
	if cur, _ := b.store.Task(id); cur.Status != "paused" {
		t.Fatalf("启动窗口内的暂停被误判为取消：%+v", cur)
	}
	h.done <- exec.Result{Status: "cancelled"}
	waitFor(t, "cancelled after cleanup", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "cancelled"
	})
}

// 假 Handle：记录 Pause/Stop，Done 由测试喂结果。
type stubHandle struct {
	done        chan exec.Result
	mu          sync.Mutex
	pause, stop int
}

func (h *stubHandle) Pause(context.Context) error {
	h.mu.Lock()
	h.pause++
	h.mu.Unlock()
	return nil
}
func (h *stubHandle) Resume(context.Context) error { return nil }
func (h *stubHandle) Stop(context.Context) error {
	h.mu.Lock()
	h.stop++
	h.mu.Unlock()
	return nil
}
func (h *stubHandle) Done() <-chan exec.Result { return h.done }
func (h *stubHandle) counts() (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pause, h.stop
}

type stubRunner struct {
	kind exec.Kind
	h    *stubHandle
}

func (r stubRunner) Kind() exec.Kind                 { return r.kind }
func (r stubRunner) Available(context.Context) error { return nil }
func (r stubRunner) Start(context.Context, exec.Request, exec.Sink) (exec.Handle, error) {
	return r.h, nil
}
