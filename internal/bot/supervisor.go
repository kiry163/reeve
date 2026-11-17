package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kiry163/reeve/internal/builtin"
	"github.com/kiry163/reeve/internal/exec"
	"github.com/kiry163/reeve/internal/exec/cli"
)

// 监工：普通 Go 代码，不调模型。负责任务队列（排队、暂停、恢复、调整顺序）、
// 盯着 agent 的事件，在需要时叫醒聊天层。

// TaskOpts 是创建任务时的选项。
type TaskOpts struct {
	Hold       bool   // true = 只记成待办（暂停状态），等用户或聊天层说"开始"再跑
	ID         string // 可选：确定性任务 ID（定时唤醒器用，避免重启后重复派发）
	ScheduleID string // 可选：来源定时计划
	Executor   string // 可选：执行器 kind；空 = 当前默认执行器（CreateTask 才补默认值）
}

type sched struct {
	mu       sync.Mutex
	launched map[string]bool     // 已交给 Manager/外部进程、还没结束/暂停的任务
	cliRuns  map[string]*cliTask // 正在运行的外部 CLI（受 mu 保护）
}

// taskSlots 是所有 Bot 共用的后台任务并发上限（MAX_CONCURRENT，默认 3）。
// 一个 Bot 没抢到空位时登记在 waiters 里，任何任务结束腾出空位都会让它们重新排一次。
type taskSlots struct {
	mu      sync.Mutex
	used    int
	max     int
	waiters map[*Bot]struct{}
}

func newTaskSlots(max int) *taskSlots {
	if max <= 0 {
		max = 3
	}
	return &taskSlots{max: max, waiters: map[*Bot]struct{}{}}
}

func (s *taskSlots) acquire(b *Bot) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used >= s.max {
		s.waiters[b] = struct{}{}
		return false
	}
	s.used++
	return true
}

func (s *taskSlots) release() {
	s.mu.Lock()
	if s.used > 0 {
		s.used--
	}
	waiters := s.waiters
	s.waiters = map[*Bot]struct{}{}
	s.mu.Unlock()
	for w := range waiters {
		go w.schedule() // 不在调用方持有的锁里排队；Bot 已关闭时 schedule 直接返回
	}
}

func (s *taskSlots) running() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used
}

// StartTask 创建任务：有空位就立刻开始，没有就排队。立刻返回 ID。
func (b *Bot) StartTask(input, title string) (string, error) {
	return b.CreateTask(input, title, TaskOpts{})
}

func (b *Bot) CreateTask(input, title string, o TaskOpts) (string, error) {
	if o.Executor == "" {
		o.Executor = b.selectedCLI() // 省略执行器 = 默认执行器；内置返回空串
	}
	return b.createTask(input, title, o, false, nil)
}

func (b *Bot) createTask(input, title string, o TaskOpts, direct bool, history []builtin.Message) (string, error) {
	rec, exists, err := b.prepareTask(input, title, o, direct, history)
	if err != nil {
		return "", err
	}
	if exists { // 同 ID 已存在：幂等，直接返回（定时器重启后的二次派发）
		b.schedule()
		return rec.ID, nil
	}
	return b.commitTask(rec), nil
}

// prepareTask 生成任务记录但先不落盘。外部派活要在写入 task.created 之前拿到用户批准，
// 批准后由 commitTask 真正建任务：审批前任务数必须为 0。
// ID 已存在时返回 exists=true，调用方只需 schedule，不要重复 append。
func (b *Bot) prepareTask(input, title string, o TaskOpts, direct bool, history []builtin.Message) (TaskRec, bool, error) {
	if strings.TrimSpace(input) == "" {
		return TaskRec{}, false, errors.New("任务内容为空")
	}
	if title == "" {
		title = defaultTitle(input)
	}
	kind := strings.TrimSpace(o.Executor)
	if kind == string(exec.KindBuiltin) {
		kind = "" // 内置执行器的记录保持空串，与旧任务一致
	}
	id := o.ID
	if id == "" {
		id = builtin.NewID("task")
	} else if _, exists := b.store.Task(id); exists {
		return TaskRec{ID: id}, true, nil
	}
	rec := TaskRec{ID: id, Title: title, Input: input, Executor: kind, DirectChat: direct, History: history,
		Status: "queued", Prio: float64(time.Now().UnixNano()), CreatedAt: time.Now()}
	if o.Hold {
		rec.Status, rec.StopReason = "paused", "held"
	}
	if o.ScheduleID != "" && !strings.HasPrefix(rec.Title, "定时·") {
		// 标题前缀让界面能看出是定时触发的
		rec.Title = "定时·" + rec.Title
	}
	return rec, false, nil
}

// commitTask 把准备好的任务写进事件日志并尝试调度。只应在审批通过后（或本来就不需要审批时）调用。
func (b *Bot) commitTask(rec TaskRec) string {
	b.store.Append("task.created", rec, false)
	b.schedule()
	return rec.ID
}

// defaultTitle 用任务原文的开头做短标题；总长（含省略号）不超过 20 个字符。
func defaultTitle(input string) string {
	title := input
	if r := []rune(title); len(r) > 20 {
		title = string(r[:19]) + "…"
	}
	return title
}

// QueuePosition 返回排队位置（1 = 下一个），不在队列里返回 0。
func (b *Bot) QueuePosition(id string) int {
	for i, t := range b.waiting() {
		if t.ID == id {
			return i + 1
		}
	}
	return 0
}

// waiting 返回真正在等空位的任务（排除已经交给 agent、只是还没发出"开始"事件的）。
func (b *Bot) waiting() []TaskRec {
	var out []TaskRec
	for _, t := range b.store.Queue() {
		if !b.isLaunched(t.ID) {
			out = append(out, t)
		}
	}
	return out
}

func (b *Bot) running() int {
	b.sch.mu.Lock()
	defer b.sch.mu.Unlock()
	return len(b.sch.launched)
}

// schedule 在有空位时按顺序启动排队的任务。任何可能腾出空位或改变队列的地方都会调用它。
func (b *Bot) schedule() {
	b.sch.mu.Lock()
	defer b.sch.mu.Unlock()
	if b.ctx.Err() != nil {
		return
	}
	for _, t := range b.store.Queue() {
		if b.sch.launched[t.ID] {
			continue
		}
		if !b.slots.acquire(b) {
			return // 全局空位满了；有任务结束时会再叫我
		}
		if t.Executor != "" {
			// 外部执行器：进程由 supervisor 直接驱动，不经过 builtin.Agent。
			b.sch.launched[t.ID] = true
			task := t
			b.runBackground(func() { b.startExternal(task) })
			continue
		}
		profile := b.botProfile()
		task := b.builtinTask(t, profile)
		if len(t.Notes) > 0 { // 暂停期间用户补充的要求
			note := builtin.Message{Role: "user", Content: "[用户在暂停期间补充] " + strings.Join(t.Notes, "\n")}
			if len(task.Resume) > 0 {
				task.Resume = append(task.Resume, note)
			} else {
				task.Input += "\n\n" + note.Content
			}
			b.store.Append("task.notes.used", t.ID, true)
		}
		b.mgr.Agent.ClearPause(t.ID)
		if _, err := b.mgr.Start(task); err != nil {
			b.slots.release()
			b.store.Append("task.updated", TaskRec{ID: t.ID, Status: "failed", Error: err.Error()}, false)
			b.finishTask(t.ID)
			continue
		}
		b.sch.launched[t.ID] = true
	}
}

// builtinTask 把存储层记录转成执行层任务。环境在任务启动时现取（含实例级/bot 级），
// 只随本次执行传入，不写进事件日志，避免密钥落盘。
func (b *Bot) builtinTask(t TaskRec, profile BotProfile) builtin.Task {
	task := builtin.Task{ID: t.ID, BotID: profile.ID, Input: t.Input, History: t.History,
		Workspace: b.cfg.Workspace, Policy: b.cfg.Policy, EnvList: b.TaskEnv(), EnvStore: b, Skills: b.skills,
		Instructions: b.instructionsFor(t, profile), MaxDuration: b.cfg.TaskMaxTime}
	if cp, ok := b.loadCheckpoint(t.ID); ok {
		task.Resume, task.StartStep = cp.Messages, cp.Steps
	}
	return task
}

// instructionsFor 组装追加给执行器的说明。内置 Agent 额外带一份密钥清单（它有 read_file 等工具）。
func (b *Bot) instructionsFor(t TaskRec, profile BotProfile) string {
	var parts []string
	if t.Executor == "" {
		if brief := b.secretsBrief(); brief != "" {
			parts = append(parts, brief)
		}
	}
	if profile.Instructions != "" {
		parts = append(parts, "职责与工作偏好：\n"+profile.Instructions)
	}
	return strings.Join(parts, "\n\n")
}

// cliTask 是一次外部运行：句柄、监工侧超时计时器，以及“为什么被监工终止”的记号。
type cliTask struct {
	h     exec.Handle
	timer *taskTimer

	mu      sync.Mutex
	killMsg string // 非空 = 监工已经决定终止它（超时/卡死且无法挂起），Done 收尾时按 failed 处理
}

// setKillMsg 记下终止原因，只有第一次生效（超时和卡死同时触发时保留先到的那个）。
func (c *cliTask) setKillMsg(msg string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.killMsg != "" {
		return false
	}
	c.killMsg = msg
	return true
}

func (c *cliTask) killed() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.killMsg
}

// taskTimer 是外部任务的监工侧超时。暂停时停表并记下剩余时间，恢复时接着走，
// 这样被 SIGSTOP 挂起的时间不算进「单任务最长运行时间」。
type taskTimer struct {
	mu      sync.Mutex
	left    time.Duration
	startAt time.Time
	timer   *time.Timer
	running bool
	fire    func()
}

func newTaskTimer(d time.Duration, fire func()) *taskTimer {
	t := &taskTimer{left: d, fire: fire}
	t.mu.Lock()
	t.armLocked()
	t.mu.Unlock()
	return t
}

func (t *taskTimer) armLocked() {
	if t.left <= 0 {
		go t.fireOnce()
		return
	}
	t.startAt = time.Now()
	t.timer = time.AfterFunc(t.left, t.fireOnce)
	t.running = true
}

func (t *taskTimer) fireOnce() {
	t.mu.Lock()
	t.running = false
	t.mu.Unlock()
	t.fire()
}

// pause 停表，把已经走过的部分从剩余时间里扣掉。
func (t *taskTimer) pause() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.running {
		return
	}
	t.timer.Stop()
	t.left -= time.Since(t.startAt)
	if t.left < 0 {
		t.left = 0
	}
	t.running = false
}

// resume 用剩余时间重新开始计时。
func (t *taskTimer) resume() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.running {
		return
	}
	t.armLocked()
}

// stop 在任务收尾时取消计时，不再触发。
func (t *taskTimer) stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.running {
		t.timer.Stop()
		t.running = false
	}
}

// cliTask 返回正在运行的外部执行器记录。
func (b *Bot) cliTask(id string) (*cliTask, bool) {
	b.sch.mu.Lock()
	defer b.sch.mu.Unlock()
	run, ok := b.sch.cliRuns[id]
	return run, ok
}

func (b *Bot) cliControl(id string, f func(h exec.Handle) error) (bool, error) {
	run, ok := b.cliTask(id)
	if !ok {
		return false, nil
	}
	return true, f(run.h)
}

// cliTimer 返回外部任务的超时计时器（没有就返回 nil）。
func (b *Bot) cliTimer(id string) *taskTimer {
	if run, ok := b.cliTask(id); ok {
		return run.timer
	}
	return nil
}

// startExternal 启动一次外部 CLI 任务。环境在这里现取：先按认证方式过滤，再给 Pi 加临时网关配置。
func (b *Bot) startExternal(t TaskRec) {
	if !b.executorAllowed(t.Executor) {
		// 第二道闸门：排队期间白名单可能已经变了。
		b.failExternal(t.ID, fmt.Errorf("执行器 %s 不在这个 Bot 的白名单里", t.Executor))
		return
	}
	kind := exec.Kind(t.Executor)
	runner, ok := b.execs.Get(kind)
	if !ok {
		b.failExternal(t.ID, fmt.Errorf("这个 Bot 没有配置执行器 %s", t.Executor))
		return
	}
	profile := b.botProfile()
	opts := b.cliOptions(t.Executor)
	env := cli.CommandEnv(os.Environ(), b.TaskEnv(), kind, opts.Auth)
	var cleanup func()
	var err error
	if kind == exec.KindPi {
		// Pi 的临时配置目录与网关令牌必须在进程结束后清理，否则会泄漏在 /tmp 里。
		env, cleanup, err = cli.ChannelEnv(env, opts)
		if err != nil {
			b.failExternal(t.ID, err)
			return
		}
	}
	input := t.Input
	if len(t.Notes) > 0 {
		input += "\n\n[用户在暂停期间补充] " + strings.Join(t.Notes, "\n")
		b.store.Append("task.notes.used", t.ID, true)
	}
	ctx, cancel := context.WithCancel(b.ctx)
	req := exec.Request{TaskID: t.ID, BotID: profile.ID, Workspace: b.cfg.Workspace, Input: input,
		Instructions: b.instructionsFor(t, profile), History: execHistory(t.History), Env: env,
		Model: opts.Model, MaxDuration: b.cfg.TaskMaxTime}
	h, err := runner.Start(ctx, req, execSink{bot: b, taskID: t.ID})
	if err != nil {
		cancel()
		if cleanup != nil {
			cleanup()
		}
		b.failExternal(t.ID, err)
		return
	}
	run := &cliTask{h: h}
	// 外部执行器不一定自己实现超时（只有 omp 收 --max-time），监工这边兜底；
	// 计时器会随暂停停表（见 taskTimer）。
	if b.cfg.TaskMaxTime > 0 {
		run.timer = newTaskTimer(b.cfg.TaskMaxTime, func() {
			if run.setKillMsg(fmt.Sprintf("超过 %s 没有完成，已终止", fmtDur(b.cfg.TaskMaxTime))) {
				_ = h.Stop(b.ctx)
			}
		})
	}
	b.sch.mu.Lock()
	b.sch.cliRuns[t.ID] = run
	b.sch.mu.Unlock()

	// 先处理“启动期间用户已经暂停/取消”的情况，再启动终态等待。
	rec, _ := b.store.Task(t.ID)
	switch {
	case rec.Status == "paused":
		// PauseTask 对“排队中”的任务只写 task.updated{paused}，不调 h.Pause；
		// 它检查 isLaunched 与写 paused 之间不原子，schedule 可能在这段间隙把任务派发出去。
		// 所以这里必须补一次 SIGSTOP（重复发送对已挂起的进程组无害）。
		_ = h.Pause(b.ctx)
		if run.timer != nil {
			run.timer.pause()
		}
	case rec.StopReason != "" && !rec.Pausing:
		_ = h.Stop(b.ctx)
	case rec.Pausing:
		if perr := h.Pause(b.ctx); perr != nil {
			b.stallPauseFailed(t.ID, perr)
			break
		}
		if run.timer != nil {
			run.timer.pause()
		}
		b.store.Append("task.updated", TaskRec{ID: t.ID, Status: "paused", StopReason: rec.StopReason}, false)
		b.store.Append("agent", builtin.Event{TaskID: t.ID, Type: builtin.EvTaskPaused, Time: time.Now(),
			Data: builtin.Result{TaskID: t.ID, Status: builtin.StatusPaused, Steps: b.taskSteps(t.ID)}}, false)
		b.announcePaused(t.ID)
	default:
		b.store.Append("agent", builtin.Event{TaskID: t.ID, Type: builtin.EvTaskStarted, Time: time.Now(),
			Data: map[string]any{"input": input, "policy": b.cfg.Policy, "workspace": b.cfg.Workspace}}, false)
		b.store.TouchProgress(t.ID) // 起点算一次进展，否则卡死检测分不清“刚启动”和“一直没输出”
	}
	b.runBackground(func() {
		res := <-h.Done()
		if run.timer != nil {
			run.timer.stop()
		}
		if cleanup != nil {
			cleanup()
		}
		cancel()
		b.sch.mu.Lock()
		delete(b.sch.cliRuns, t.ID)
		b.sch.mu.Unlock()
		if msg := run.killed(); msg != "" { // 超时或卡死且无法挂起：监工主动终止，按失败收尾
			b.terminalTask(t.ID, builtin.Result{TaskID: t.ID, Status: builtin.StatusFailed,
				Steps: b.taskSteps(t.ID), Error: msg})
			return
		}
		// 优雅停机（SIGTERM）：进程是我们自己停的，不是用户取消。res.Interrupted 由 cli.Proc
		// 打标（任何 Stop 打断的运行）；只有它 + 本进程正在停机时才落 paused/interrupted，
		// 让重启后的 recover() 幂等跳过，界面显示「已中断，需要重新执行」。
		// 用户的取消（StopReason=user）不覆盖，仍按 res 的真实结果收尾。
		if res.Interrupted && b.shuttingDown.Load() && b.markInterruptedOnShutdown(t.ID) {
			return
		}
		b.finishExternal(t.ID, res)
	})
}

// markInterruptedOnShutdown 把被停机打断的外部任务落成 paused/interrupted：
// 不写 cancelled、不汇报（Reported 保持 false），重启后不自动重跑，需要用户重新派。
// 返回 false 表示不该改写（不是停机、已经终态、或用户的取消正在进行），调用方照常收尾。
func (b *Bot) markInterruptedOnShutdown(id string) bool {
	if !b.shuttingDown.Load() {
		return false
	}
	rec, ok := b.store.Task(id)
	if !ok || terminal(rec.Status) {
		return false
	}
	if rec.Status == "running" && rec.StopReason == "user" && !rec.Pausing {
		// 只有真正的「取消在途」才让取消路径照常写 cancelled。用户暂停同样写 StopReason=user，
		// 但停机打断的是暂停中的进程，必须落成可恢复的中断，否则重启后既不能继续也收不到说明。
		return false
	}
	b.store.Append("task.updated", TaskRec{ID: id, Status: "paused", StopReason: "interrupted"}, false)
	b.expireApprovals(id, "任务已中断")
	b.guard.Revoke(id) // session 批准只在任务运行期间有效
	b.landed(id)       // 进程没了：归还并发槽、清掉 launched（stop() 也用于删除 Bot，槽位是全局共享的）
	// 不写调度器 claim：停机中断等同普通暂停，claim 保持 dispatched 才算「未完成运行」；
	// 写 paused 会让 OpenClaimsFor/recoverLocked 认不出它，之后完成也写不进 FinishedAt。
	return true
}

func (b *Bot) failExternal(id string, err error) {
	status := builtin.StatusFailed
	if rec, ok := b.store.Task(id); ok && rec.StopReason != "" {
		status = builtin.StatusCancelled
	}
	b.terminalTask(id, builtin.Result{TaskID: id, Status: status, Steps: b.taskSteps(id), Error: err.Error()})
}

func (b *Bot) finishExternal(id string, res exec.Result) {
	status := builtin.Status(res.Status)
	switch status {
	case builtin.StatusCompleted, builtin.StatusCancelled:
	default:
		status = builtin.StatusFailed
	}
	b.terminalTask(id, builtin.Result{TaskID: id, Status: status, Output: res.Output,
		Steps: b.taskSteps(id), Error: res.Error})
}

// taskSteps 返回当前任务已经落盘的工具调用数（外部执行器没有 Agent 的步数计数器）。
func (b *Bot) taskSteps(id string) int {
	if rec, ok := b.store.Task(id); ok {
		return len(rec.StepLog)
	}
	return 0
}

func execHistory(msgs []builtin.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		out = append(out, fmt.Sprintf("[%s]\n%s", m.Role, m.Content))
	}
	return out
}

// execSink 把外部 CLI 的事件翻译成既有 builtin.Event，客户端事件契约保持不变。
type execSink struct {
	bot    *Bot
	taskID string
}

func (s execSink) Emit(e exec.Event) { s.bot.emitExecEvent(s.taskID, e) }

func (b *Bot) emitExecEvent(taskID string, e exec.Event) {
	switch e.Kind {
	case exec.EventText:
		// 流式文字只推给界面，不落盘；完整结果在 task.completed 的 Output 里。
		// 有文字输出就是有进展（心跳不算，见 TouchProgress）。
		b.store.TouchProgress(taskID)
		b.store.Broadcast("agent", builtin.Event{TaskID: taskID, Type: builtin.EvTextDelta, Time: time.Now(),
			Data: map[string]any{"text": e.Text}})
	case exec.EventHeartbeat:
		b.store.Touch(taskID)
		b.store.Broadcast("agent", builtin.Event{TaskID: taskID, Type: builtin.EvHeartbeat, Time: time.Now()})
	case exec.EventTool:
		args := json.RawMessage(e.Input)
		if !json.Valid(args) {
			raw, _ := json.Marshal(map[string]string{"input": e.Input})
			args = raw
		}
		b.store.Append("agent", builtin.Event{TaskID: taskID, Type: builtin.EvToolCall, Time: time.Now(),
			Data: map[string]any{"call_id": "cli-" + e.Tool, "tool": e.Tool, "args": args}}, false)
		b.store.TouchProgress(taskID)
	case exec.EventToolResult:
		output := e.Output
		if e.Err != "" && output == "" {
			output = e.Err
		}
		b.store.Append("agent", builtin.Event{TaskID: taskID, Type: builtin.EvToolResult, Time: time.Now(),
			Data: map[string]any{"call_id": "cli-" + e.Tool, "tool": e.Tool,
				"output": builtin.Truncate(output, 16000), "error": e.Err != "", "denied": false}}, false)
		b.store.TouchProgress(taskID)
	}
}

func (b *Bot) landed(id string) {
	b.sch.mu.Lock()
	was := b.sch.launched[id]
	delete(b.sch.launched, id)
	b.sch.mu.Unlock()
	if was {
		b.slots.release()
	}
}

// PauseTask 暂停任务：运行中的在下一个安全点暂停（正在执行的命令/写文件一定会跑完），排队中的直接挂起。
// by = user | chat。等待中的审批保持不变（没有时间上限），处理完这一步后暂停。
func (b *Bot) PauseTask(id, by string) (string, error) {
	t, ok := b.findTaskRec(id)
	if !ok {
		return "", fmt.Errorf("没有这个任务: %s", id)
	}
	switch {
	case t.Status == "running" || (t.Status == "queued" && b.isLaunched(t.ID)):
		if t.Pausing {
			return "这个任务已经在暂停中了。", nil
		}
		b.store.Append("task.pausing", TaskRec{ID: t.ID, StopReason: by}, false)
		if detected, err := b.cliControl(t.ID, func(h exec.Handle) error { return h.Pause(b.ctx) }); detected {
			if err != nil {
				// 挂起失败不能停在“已请求暂停”：撤掉标记，把限制如实告诉用户。
				b.store.Append("task.updated", TaskRec{ID: t.ID, Status: "running", ClearStopReason: true}, false)
				if errors.Is(err, cli.ErrSignalUnsupported) {
					return "", errors.New("该执行器在 Windows 上不支持挂起，请用停止代替")
				}
				return "", fmt.Errorf("暂停失败：%w", err)
			}
			if tm := b.cliTimer(t.ID); tm != nil {
				tm.pause() // 挂起期间不算进单任务超时
			}
			b.store.Append("task.updated", TaskRec{ID: t.ID, Status: "paused", StopReason: by}, false)
			b.store.Append("agent", builtin.Event{TaskID: t.ID, Type: builtin.EvTaskPaused, Time: time.Now(),
				Data: builtin.Result{TaskID: t.ID, Status: builtin.StatusPaused, Steps: b.taskSteps(t.ID)}}, false)
			b.announcePaused(t.ID)
			return "已暂停：外部执行器的进程已挂起，继续时会从原处接着跑。", nil
		}
		_ = b.mgr.Pause(t.ID)
		if t.Awaiting > 0 {
			return "已请求暂停。它正在等用户审批：审批卡片保持有效，处理完这一步后就会暂停。", nil
		}
		return "已请求暂停：当前这一步做完后（不会打断正在执行的命令或写入）就停下，进度会保存。", nil
	case t.Status == "queued":
		b.store.Append("task.updated", TaskRec{ID: t.ID, Status: "paused", StopReason: by}, false)
		return "已把排队中的任务挂起，不会自动开始。", nil
	case t.Status == "paused":
		return "这个任务本来就是暂停状态。", nil
	}
	return "", fmt.Errorf("任务已经%s，不能暂停", statusCN(t.Status))
}

func (b *Bot) isLaunched(id string) bool {
	b.sch.mu.Lock()
	defer b.sch.mu.Unlock()
	return b.sch.launched[id]
}

// ResumeTask 让暂停的任务继续：放回队列最前面，有空位马上接着跑（从保存的进度继续）。
func (b *Bot) ResumeTask(id, note string) (string, error) {
	t, ok := b.findTaskRec(id)
	if !ok {
		return "", fmt.Errorf("没有这个任务: %s", id)
	}
	if t.Status != "paused" {
		return "", fmt.Errorf("任务现在是%s，不是暂停状态", statusCN(t.Status))
	}
	if t.Executor != "" && !b.executorAllowed(t.Executor) {
		// 白名单变了：不恢复，保持在 paused，用户仍可改回白名单或取消。
		return "", fmt.Errorf("执行器 %s 已不在这个 Bot 的白名单里，任务保持暂停（可用：%s）",
			t.Executor, strings.Join(b.AllowedExecutors(), "、"))
	}
	note = strings.TrimSpace(note)
	// 外部执行器：进程只是被 SIGSTOP 挂起，恢复它而不是重新排队（否则会再起一个进程）。
	if detected, err := b.cliControl(t.ID, func(h exec.Handle) error { return h.Resume(b.ctx) }); detected {
		if err != nil {
			return "", fmt.Errorf("继续失败：%w", err)
		}
		if tm := b.cliTimer(t.ID); tm != nil {
			tm.resume() // 用剩余时间接着计时
		}
		// 清掉暂停原因：卡死检测要求运行中的任务 StopReason 为空，否则恢复后再也检测不到。
		b.store.Append("task.updated", TaskRec{ID: t.ID, Status: "running", ClearStopReason: true}, false)
		// 补一条 started：界面靠它计算用时；StartedAt 非零时 store 会保留首次开始时间。
		b.store.Append("agent", builtin.Event{TaskID: t.ID, Type: builtin.EvTaskStarted, Time: time.Now(),
			Data: map[string]any{"input": t.Input, "policy": b.cfg.Policy, "workspace": b.cfg.Workspace}}, false)
		b.store.TouchProgress(t.ID) // 恢复后重新开始计“多久没有输出”
		if note != "" {
			// 外部执行器的输入在启动时就固定了：补充要求无法转达，也不假装记下来。
			return "已继续，外部执行器从挂起处接着跑。外部执行器运行中无法插话，补充要求没有转达；要改要求请取消后重新派一次。", nil
		}
		return "已继续，外部执行器从挂起处接着跑。", nil
	}
	if note != "" {
		b.store.Append("task.note", map[string]string{"id": t.ID, "text": note}, false)
	}
	b.store.Append("task.updated", TaskRec{ID: t.ID, Status: "queued", Prio: b.frontPrio()}, false)
	b.schedule()
	if b.isLaunched(t.ID) {
		return "已继续，从上次保存的进度接着做。", nil
	}
	return fmt.Sprintf("已放回队列第 %d 位，有空位就接着做。", b.QueuePosition(t.ID)), nil
}

func (b *Bot) frontPrio() float64 {
	q := b.waiting()
	if len(q) == 0 {
		return float64(time.Now().UnixNano())
	}
	return q[0].Prio - 1e6
}

// Reprioritize 调整排队顺序：position 1 = 下一个运行。
func (b *Bot) Reprioritize(id string, position int) (string, error) {
	q := b.waiting()
	idx := -1
	for i, t := range q {
		if t.ID == id || strings.HasSuffix(t.ID, id) {
			idx = i
		}
	}
	if idx < 0 {
		return "", errors.New("这个任务不在排队中")
	}
	me := q[idx]
	rest := append(append([]TaskRec(nil), q[:idx]...), q[idx+1:]...)
	position = max(1, min(position, len(q)))
	var prio float64
	switch {
	case len(rest) == 0:
		prio = me.Prio
	case position == 1:
		prio = rest[0].Prio - 1e6
	case position > len(rest):
		prio = rest[len(rest)-1].Prio + 1e6
	default:
		prio = (rest[position-2].Prio + rest[position-1].Prio) / 2
	}
	b.store.Append("task.prio", TaskRec{ID: me.ID, Prio: prio}, false)
	b.schedule()
	return fmt.Sprintf("已调整：「%s」现在排第 %d 位。", me.Title, b.QueuePosition(me.ID)), nil
}

// CancelTask 停止一个任务（运行中、排队中、暂停中都可以）。
func (b *Bot) CancelTask(id, reason string) error {
	t, ok := b.findTaskRec(id)
	if !ok {
		return fmt.Errorf("没有这个任务: %s", id)
	}
	if !t.Open() {
		return fmt.Errorf("任务已经%s", statusCN(t.Status))
	}
	b.store.Append("task.updated", TaskRec{ID: t.ID, StopReason: reason}, false)
	// 和 schedule 用同一把锁：检查"还没开始"和标记取消之间，不能被别的 goroutine 抢先派发出去。
	b.sch.mu.Lock()
	launched := b.sch.launched[t.ID]
	if !launched {
		b.store.Append("task.updated", TaskRec{ID: t.ID, Status: "cancelled", Error: "在开始前被取消"}, false)
		b.sch.mu.Unlock()
		b.finishTask(t.ID)
	} else {
		b.sch.mu.Unlock()
	}
	if launched {
		if detected, err := b.cliControl(t.ID, func(h exec.Handle) error { return h.Stop(b.ctx) }); detected {
			return err // 终态由 Done 后的统一收尾发出
		}
		if t.Executor != "" {
			// 进程还在启动、句柄尚未登记：取消原因已经写进 StopReason，
			// startExternal 启动完会看到它并立刻停止。
			return nil
		}
		return b.mgr.Cancel(t.ID) // 结束事件由 agent 发出，之后统一汇报
	}
	b.removeCheckpoint(t.ID)
	go b.report(t.ID)
	return nil
}

// RetryTask 用同样的要求、同一个执行器重新开一个任务（原任务如果还在跑会先停掉）。
// 这是用户点击，不再弹派活审批；白名单在重试时重新校验，已被移除的执行器直接报错。
func (b *Bot) RetryTask(id string) (string, error) {
	t, ok := b.findTaskRec(id)
	if !ok {
		return "", fmt.Errorf("没有这个任务: %s", id)
	}
	kind := strings.TrimSpace(t.Executor)
	if kind != "" && !b.executorAllowed(kind) {
		return "", fmt.Errorf("执行器 %s 已不在这个 Bot 的白名单里，没法按原执行器重试（可用：%s）",
			kind, strings.Join(b.AllowedExecutors(), "、"))
	}
	if t.Open() {
		_ = b.CancelTask(t.ID, "user")
	}
	return b.createTask(t.Input, t.Title, TaskOpts{Executor: kind}, t.DirectChat && kind != "", t.History)
}

func (b *Bot) findTaskRec(id string) (TaskRec, bool) {
	if t, ok := b.store.Task(id); ok {
		return t, true
	}
	if id = strings.TrimSpace(id); len(id) >= 4 {
		for _, t := range b.store.Tasks() {
			if strings.HasSuffix(t.ID, id) {
				return t, true
			}
		}
	}
	return TaskRec{}, false
}

func statusCN(s string) string {
	return map[string]string{"queued": "在排队", "running": "在运行", "paused": "暂停中", "completed": "完成了",
		"failed": "失败了", "cancelled": "被停止了"}[s]
}

// ---------- 进度快照 ----------

type checkpoint struct {
	Messages []builtin.Message `json:"messages"`
	Steps    int               `json:"steps"`
}

func (b *Bot) checkpointPath(id string) string {
	return filepath.Join(b.cfg.DataDir, "checkpoints", filepath.Base(id)+".json")
}

func (b *Bot) saveCheckpoint(id string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	raw = b.redactor.RedactBytes(raw)
	p := b.checkpointPath(id)
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	tmp := p + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Remove(p)
		_ = os.Rename(tmp, p)
	}
}

func (b *Bot) loadCheckpoint(id string) (checkpoint, bool) {
	var cp checkpoint
	raw, err := os.ReadFile(b.checkpointPath(id))
	if err != nil {
		return cp, false
	}
	raw = b.redactor.RedactBytes(raw) // 旧检查点可能写于脱敏规则补全之前
	if json.Unmarshal(raw, &cp) != nil || len(cp.Messages) == 0 {
		return cp, false
	}
	return cp, true
}

func (b *Bot) removeCheckpoint(id string) { _ = os.Remove(b.checkpointPath(id)) }

// ---------- agent 事件 ----------

func (b *Bot) onAgentEvent(e builtin.Event) {
	switch e.Type {
	case builtin.EvHeartbeat, builtin.EvThinkingDelta:
		if e.Type == builtin.EvThinkingDelta {
			b.store.Touch(e.TaskID) // 模型还在持续思考就不是卡死
		}
		b.store.Broadcast("agent", e) // 心跳和思考片段只推送，不保存
		return
	case builtin.EvCheckpoint:
		b.saveCheckpoint(e.TaskID, e.Data) // 快照单独存文件，不进事件日志
		return
	}
	if e.Type == builtin.EvTextDelta {
		// agent 的流式文字只推给界面，不写进事件日志（结果在 task.completed 里有完整的一份）。
		b.store.Touch(e.TaskID)
		b.store.Broadcast("agent", e)
		return
	}
	if e.Terminal() {
		res, _ := e.Data.(builtin.Result)
		if res.Status == "" {
			res.Status = statusOfEvent(e.Type)
		}
		b.terminalTask(e.TaskID, res) // 终态只在这里落盘与收尾
		return
	}
	b.store.Append("agent", e, false)
	switch {
	case e.Type == builtin.EvTaskPaused:
		b.landed(e.TaskID)
		b.announcePaused(e.TaskID)
	case e.Type == builtin.EvApprovalRequested:
		if r, ok := e.Data.(builtin.ApprovalRequest); ok {
			t, ok := b.store.Task(e.TaskID)
			if !ok {
				return // 聊天层直接发起的审批（chat_ 前缀）没有对应任务，卡片本身已经显示给用户
			}
			if t.DirectChat {
				return // 审批卡片本身已显示在对话里
			}
			b.notify(fmt.Sprintf("任务「%s」(%s) 需要用户批准才能继续：%s %s。原因：%s。审批卡片已经显示给用户。用一句话说明为什么需要确认；不要替用户批准，也不要催。",
				t.Title, t.ID, r.ToolName, builtin.Truncate(redactApprovalArgs(r.Args), 500), r.Reason))
		}
	}
}

// announcePaused 在后台向聊天层说明暂停原因（内置与外部执行器共用）。
// 调用方负责释放槽位（内置）或保留槽位（外部进程挂起）。
func (b *Bot) announcePaused(id string) {
	b.runBackground(func() {
		t, _ := b.store.Task(id)
		switch t.StopReason {
		case "chat":
			b.notify(fmt.Sprintf("任务「%s」(%s) 已在安全点暂停，进度已保存（第 %d 步）。之后可以用 resume_task 继续。", t.Title, t.ID, t.Steps))
		case "stall":
			if t.Executor != "" {
				b.notify(fmt.Sprintf("任务「%s」(%s) 超过 %s 没有任何输出，已挂起外部进程（SIGSTOP），进程还在，恢复会从原处继续（第 %d 步）。请如实告诉用户，并给出下一步建议（继续、重试或换个做法）。", t.Title, t.ID, fmtDur(b.cfg.ExternalStallTimeout), t.Steps))
			} else {
				b.notify(fmt.Sprintf("任务「%s」(%s) 超过 %s 没有任何进展，已在安全点暂停，进度已保存（第 %d 步）。请如实告诉用户，并给出下一步建议（继续、重试或换个做法）。", t.Title, t.ID, fmtDur(b.cfg.StallTimeout), t.Steps))
			}
		default:
			b.contextNote(fmt.Sprintf("用户在界面上暂停了任务「%s」(%s)，进度已保存。", t.Title, t.ID))
		}
		b.schedule()
	})
}

// terminalTask 是内置与外部执行器共用的终态收尾：落一条终态事件，然后统一释放并发槽、
// 作废审批、清理检查点并汇报。调用方必须保证每个任务只调用一次。
func (b *Bot) terminalTask(id string, res builtin.Result) {
	if res.TaskID == "" {
		res.TaskID = id
	}
	typ := map[builtin.Status]builtin.EventType{
		builtin.StatusCompleted: builtin.EvTaskCompleted,
		builtin.StatusFailed:    builtin.EvTaskFailed,
		builtin.StatusCancelled: builtin.EvTaskCancelled,
	}[res.Status]
	if typ == "" {
		typ, res.Status = builtin.EvTaskFailed, builtin.StatusFailed
	}
	b.store.Append("agent", builtin.Event{TaskID: id, Type: typ, Time: time.Now(), Data: res}, false)
	b.expireApprovals(id, "任务已结束")
	b.guard.Revoke(id) // session 批准只在任务运行期间有效
	b.landed(id)
	// 只有正常完成和用户主动取消才删检查点；失败/超时/卡死/中断都保留，方便排查或续跑。
	if t, ok := b.store.Task(id); ok && (t.Status == "completed" || t.Status == "cancelled" && t.StopReason == "user") {
		b.removeCheckpoint(id)
	}
	if b.sched != nil {
		st := string(res.Status)
		if t, ok := b.store.Task(id); ok {
			st = t.Status
		}
		b.sched.MarkTaskFinished(id, st)
	}
	b.runBackground(func() { b.report(id); b.schedule() }) // 不在执行器 goroutine 里做重活
}

func statusOfEvent(t builtin.EventType) builtin.Status {
	switch t {
	case builtin.EvTaskCompleted:
		return builtin.StatusCompleted
	case builtin.EvTaskCancelled:
		return builtin.StatusCancelled
	default:
		return builtin.StatusFailed
	}
}

// redactApprovalArgs 返回给聊天层看的审批参数：带 value 的工具（set_env 等）
// 把值替换成占位符，避免明文值进入模型上下文。事件里的审批记录保持原样，
// 网页卡片仍然显示真实值供用户判断。
func redactApprovalArgs(args json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return string(args)
	}
	if _, ok := m["value"]; !ok {
		return string(args)
	}
	m["value"] = "<已隐藏>"
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false) // 保留 <已隐藏> 的字面形式，不要变成 \u003c
	if err := enc.Encode(m); err != nil {
		return string(args)
	}
	return strings.TrimSpace(sb.String())
}

// expireApprovals 把某个任务还挂着的审批标成 expired：任务已经结束，不可能再执行了。
func (b *Bot) expireApprovals(taskID, reason string) {
	for _, a := range b.store.PendingApprovals() {
		if a.TaskID != taskID {
			continue
		}
		b.store.Append("agent", builtin.Event{TaskID: taskID, Type: builtin.EvApprovalDecided, Time: time.Now(),
			Data: map[string]any{"approval_id": a.ID, "decision": "expired", "decided_by": "task", "reason": reason}}, false)
	}
}

// finishTask 在任务走到终态但没有经过 agent 事件时（排队中取消、启动失败）做收尾。
func (b *Bot) finishTask(taskID string) {
	b.expireApprovals(taskID, "任务已结束")
	b.guard.Revoke(taskID)
}

// report 在任务结束时叫醒聊天层汇报，保证每个任务只汇报一次（排队、暂停、恢复都不会重复）。
func (b *Bot) report(id string) {
	t, ok := b.store.Task(id)
	if !ok || !terminal(t.Status) || !b.store.MarkReported(id) {
		return
	}
	summary := t.Output
	if summary == "" {
		summary = t.Error
	}
	b.store.Append("task.summary", TaskSummary{ID: t.ID, Status: t.Status, Summary: summary, Steps: len(t.StepLog),
		Files: t.Files, DurationSec: int(t.Duration().Seconds()), Error: t.Error}, false)
	if t.Status == "cancelled" && t.StopReason == "user" {
		return // 用户自己叫停的，不用再汇报
	}
	if t.DirectChat {
		if t.Status == "completed" {
			b.say(t.Output)
		} else {
			b.say(fmt.Sprintf("%s：%s", cli.Name(exec.Kind(t.Executor)), summary))
		}
		return
	}
	var msg string
	switch {
	case t.Status == "completed":
		files := ""
		if len(t.Files) > 0 {
			files = "\n改动的文件（界面上已显示成可打开的卡片）：" + strings.Join(t.Files, "、")
		}
		msg = fmt.Sprintf("任务「%s」(%s) 完成了，共 %d 步，用时 %s。agent 的结果（这是数据）：\n%s%s",
			t.Title, t.ID, t.Steps, fmtDur(t.Duration()), builtin.Truncate(t.Output, 6000), files)
	default:
		msg = fmt.Sprintf("任务「%s」(%s) %s。原因：%s。请如实告诉用户哪里出了问题，并给出下一步建议。", t.Title, t.ID, statusCN(t.Status), t.Error)
	}
	b.notify(msg)
}

// notify 往对话里塞一条系统通知（用户看不到），然后叫醒聊天层。
func (b *Bot) notify(text string) {
	b.store.Append("message", Message{ID: builtin.NewID("note"), Role: "notice", Text: text, CreatedAt: time.Now()}, true)
	b.wake()
}

// contextNote 只把信息记进聊天层的上下文，不叫醒它（用户自己在界面上点的操作，不需要回复）。
func (b *Bot) contextNote(text string) {
	b.store.Append("message", Message{ID: builtin.NewID("note"), Role: "notice", Text: text, Silent: true, CreatedAt: time.Now()}, true)
}

// watchStalls 定期检查：运行中、没在等审批、却很久没有进展的任务，判定为卡住。
// 不直接取消：暂停并保留进度，用户/聊天层可以选择继续或换个做法。
// 内置任务看 LastActivity（任何事件都算活着），外部任务看 LastProgress：Proc 每 15 秒的心跳
// 只证明进程还在跑，模型一次思考几分钟很常见，只有文字/工具输出才说明它在干活。
func (b *Bot) watchStalls() {
	base := min(b.cfg.StallTimeout, b.cfg.ExternalStallTimeout)
	tk := time.NewTicker(min(base/4, 15*time.Second))
	defer tk.Stop()
	for {
		select {
		case <-b.ctx.Done():
			return
		case <-tk.C:
			for _, t := range b.store.Tasks() {
				if t.Status != "running" || t.Pausing || t.Awaiting > 0 {
					continue
				}
				if t.Executor == "" {
					if time.Since(t.LastActivity) <= b.cfg.StallTimeout {
						continue
					}
					b.store.Append("task.note", map[string]string{"id": t.ID,
						"text": fmt.Sprintf("已经 %s 没有任何进展，监工请求暂停。进度会保留；恢复后从断点继续，如果反复卡住请换个做法。", fmtDur(b.cfg.StallTimeout))}, false)
					b.store.Append("task.pausing", TaskRec{ID: t.ID, StopReason: "stall"}, false)
					_ = b.mgr.Pause(t.ID) // 安全点暂停：正在执行的命令/写入会跑完，检查点照常保存
					continue
				}
				if t.LastProgress.IsZero() || time.Since(t.LastProgress) <= b.cfg.ExternalStallTimeout {
					continue // 重放出来的任务没有进度时间：等第一条输出（或 task.started）再算
				}
				b.stallExternal(t.ID)
			}
		}
	}
}

// stallExternal 卡死外部任务：没有安全点可用，直接 SIGSTOP 进程组并落定 paused(stall)。
// 进程还活着、只是挂起，并发槽继续占着（恢复后从原处接着跑，不会重起一个进程）。
// 取消有 5 秒 TERM→KILL 宽限期，状态随时可能被改掉，所以落定前在同一把 sch.mu 下复核。
func (b *Bot) stallExternal(id string) {
	// 先在同一把锁下确认它仍在跑且没被取消/暂停，再写 stall 标记。
	b.sch.mu.Lock()
	rec, ok := b.store.Task(id)
	if !ok || rec.Status != "running" || rec.Pausing || rec.Awaiting > 0 || rec.StopReason != "" {
		b.sch.mu.Unlock()
		return
	}
	b.store.Append("task.note", map[string]string{"id": id,
		"text": fmt.Sprintf("已经 %s 没有任何输出，监工请求挂起外部进程。进程还在，恢复会从原处继续；如果反复卡住请换个做法。", fmtDur(b.cfg.ExternalStallTimeout))}, false)
	b.store.Append("task.pausing", TaskRec{ID: id, StopReason: "stall"}, false)
	run, live := b.sch.cliRuns[id]
	b.sch.mu.Unlock()
	if !live {
		return // 句柄还没登记：startExternal 会看到 pausing 并补一次 SIGSTOP
	}
	if err := run.h.Pause(b.ctx); err != nil {
		b.stallPauseFailed(id, err)
		return
	}
	if !b.stallStillSettled(id) {
		return // 挂起途中任务被取消/收尾了：让那条路径去写终态
	}
	if run.timer != nil {
		run.timer.pause() // 挂起期间不算进单任务超时
	}
	b.store.Append("task.updated", TaskRec{ID: id, Status: "paused", StopReason: "stall"}, false)
	b.store.Append("agent", builtin.Event{TaskID: id, Type: builtin.EvTaskPaused, Time: time.Now(),
		Data: builtin.Result{TaskID: id, Status: builtin.StatusPaused, Steps: b.taskSteps(id)}}, false)
	b.announcePaused(id)
}

// stallStillSettled 在 sch.mu 下复核：任务还是自己标的那次 stall（没被取消/用户暂停覆盖），进程也还在。
func (b *Bot) stallStillSettled(id string) bool {
	b.sch.mu.Lock()
	defer b.sch.mu.Unlock()
	rec, ok := b.store.Task(id)
	if !ok || rec.Status != "running" || rec.StopReason != "stall" {
		return false
	}
	_, live := b.sch.cliRuns[id]
	return live
}

// stallPauseFailed 处理挂起进程失败。不能留下“记录说暂停、进程却还在跑”的状态：
//   - 监工卡死 + 平台不支持挂起：挂不住就终止，按 failed 收尾；
//   - 用户请求的暂停失败：撤掉 pausing，如实说明该执行器不支持挂起，任务继续跑。
func (b *Bot) stallPauseFailed(id string, err error) {
	rec, _ := b.store.Task(id)
	if errors.Is(err, cli.ErrSignalUnsupported) && rec.StopReason == "stall" {
		b.terminateStalled(id, "长时间没有进展，且当前平台不支持挂起，已终止")
		return
	}
	// 清掉 pausing 与暂停原因，下个周期还能重新判定；进程状态不变，就当这次挂起没发生。
	b.store.Append("task.updated", TaskRec{ID: id, Status: "running", ClearStopReason: true}, false)
	b.store.Append("task.note", map[string]string{"id": id,
		"text": fmt.Sprintf("挂起外部进程失败（%v），当前平台或该执行器不支持挂起，任务继续运行；如果长时间没有输出，可以取消后重新派一次。", err)}, false)
}

// terminateStalled 卡死却挂不住时直接终止进程：终态由 Done 收尾，按 failed 处理。
func (b *Bot) terminateStalled(id, msg string) {
	run, ok := b.cliTask(id)
	if !ok {
		return
	}
	if run.timer != nil {
		run.timer.stop()
	}
	if run.setKillMsg(msg) {
		_ = run.h.Stop(b.ctx)
	}
}

// openScheduleClaim 判断任务是否还有未闭合的定时认领（即定时计划派生的任务）。
func (b *Bot) openScheduleClaim(taskID string) bool {
	if b.sched == nil {
		return false
	}
	for _, c := range b.sched.Store.Claims() {
		if c.TaskID == taskID && (c.Status == "claimed" || c.Status == "dispatched") {
			return true
		}
	}
	return false
}

// recover 在启动时处理上次没收尾的东西：
//   - 运行中和排队中的任务都改成"暂停"，由用户决定是否继续，而不是自动开跑；
//     内置任务有检查点，标 restart；外部任务的进程随上次退出一起没了，标 interrupted，不自动重跑；
//   - 未决审批保持有效（不当作失效），已作出的批准记录重新注入审批管线；
//   - 没汇报的结果补汇报。
//
// 上次留下的外部 CLI 进程组不在这里清理（可能已不存在，也可能不属于本实例），见 spec 的已知边界。
func (b *Bot) recover() {
	var restarted, interrupted, interruptedScheduled []string
	for _, t := range b.store.Tasks() {
		if t.Status != "running" && t.Status != "queued" && !t.Pausing {
			continue
		}
		reason := "restart"
		if t.Executor != "" && t.Executor != string(exec.KindBuiltin) {
			reason = "interrupted"
		}
		b.store.Append("task.updated", TaskRec{ID: t.ID, Status: "paused", StopReason: reason}, false)
		label := fmt.Sprintf("「%s」(%s)", t.Title, t.ID)
		switch {
		case reason != "interrupted":
			restarted = append(restarted, label)
		case b.openScheduleClaim(t.ID):
			// 定时计划派生：claim 还开着，重新派一次会留下未闭合认领，overlap=skip 会一直跳过。
			interruptedScheduled = append(interruptedScheduled, label)
		default:
			interrupted = append(interrupted, label)
		}
	}
	b.guard.Restore(b.store.ApprovalRecords())
	if len(restarted) > 0 {
		b.notify(fmt.Sprintf("服务重启了，这些任务被暂停（进度已保存，可以继续）：%s。简短告诉用户，并问是否继续（用 ask_user 卡片，选项如「继续」「先不用」）。", strings.Join(restarted, "、")))
	}
	if len(interrupted) > 0 {
		b.notify(fmt.Sprintf("服务重启了，这些外部执行器任务已中断（不会自动重跑，需要请重新派一次）：%s。简短告诉用户它被中断了、没有自动重跑，需要的话用 start_task 按原要求重新派一次。", strings.Join(interrupted, "、")))
	}
	if len(interruptedScheduled) > 0 {
		b.notify(fmt.Sprintf("服务重启了，这些由定时计划派生的外部任务已中断（不会自动重跑）：%s。请让用户选择恢复（继续）或取消它；否则它对应的定时计划会一直跳过后续触发。", strings.Join(interruptedScheduled, "、")))
	}
	for _, t := range b.store.Tasks() {
		if !t.Reported && terminal(t.Status) {
			b.report(t.ID)
		}
	}
}
