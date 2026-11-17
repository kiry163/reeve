// Package cli 把 codex / pi / omp 这类外部 CLI 包成 exec.Runner。
//
// 进程控制（start / 逐行扫描 / 进程组 / 暂停恢复 / 终止 / 心跳）集中在 Proc，
// 每个适配器只负责把 CLI 的私有输出格式翻译成 exec.Event。
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/kiry163/reeve/internal/envx"
	"github.com/kiry163/reeve/internal/exec"
)

const (
	maxCLIOutput     = 1 << 20 // 单次运行保留的文本上限（与旧路径一致）
	scanLineLimit    = maxCLIOutput
	stderrTailLimit  = 8 * 1024
	resultTextLimit  = 16000
	errorDetailLimit = 1400
	stopGrace        = 5 * time.Second
)

// heartbeatInterval 是心跳间隔；测试会把它调小。
var heartbeatInterval = 15 * time.Second

// ErrSignalUnsupported 表示当前平台不支持某个进程信号（Windows 的挂起/恢复/TERM）。
var ErrSignalUnsupported = errors.New("该平台不支持暂停/终止进程信号")

// errLineTooLong 是单行输出超过扫描上限时的诊断：这一行及其后的流已经不可信。
var errLineTooLong = fmt.Errorf("CLI 输出单行超过 %d MiB，已截断后续读取", scanLineLimit>>20)

// Spec 描述一次 CLI 启动。Parse 按行消费 stdout，把 CLI 的私有格式翻成 exec.Event；
// Done 在进程退出、管道读完之后调用，给需要整段收尾的适配器（pi）flush。
type Spec struct {
	Kind      exec.Kind
	Exe       string
	Args      []string
	Stdin     string
	Dir       string
	Env       []string // nil 表示继承进程环境（已去掉 reeve 自身密钥）
	MaxOutput int      // 保留的输出上限，<=0 用 maxCLIOutput
	Parse     func(line []byte, sink exec.Sink, fail func(string))
	Done      func(sink exec.Sink)
	// FinalText 可选：返回这次运行的最终回复。codex 用它只保留最后一条 agent_message；
	// 为空时用 EventText 累积出来的文本。
	FinalText func() string
	// Diagnostic 可选：进程非零退出且 stderr 为空时补充的说明（例如 codex 的重连提示）。
	// 它只影响错误文案，不改变“是否失败”的判定。
	Diagnostic func() string
}

// Proc 是正在运行的 CLI 进程组。Pause/Resume 用进程组信号，Stop 先 TERM 再 KILL。
type Proc struct {
	kind   exec.Kind
	pgid   int
	done   chan exec.Result
	exited chan struct{}
	sink   exec.Sink

	mu       sync.Mutex // 停止状态与解析器报错
	stopping bool
	errText  string

	emitMu sync.Mutex // 保证事件顺序；sink 不要求并发安全
	text   strings.Builder
	limit  int
	final  func() string
	diag   func() string
	sealed bool // 结果发布后不再发心跳，避免终态事件之后又冒出事件
}

var _ exec.Handle = (*Proc)(nil)

// Launch 启动 CLI 并开始逐行消费它的输出。
func Launch(ctx context.Context, spec Spec, sink exec.Sink) (*Proc, error) {
	if err := validateSpec(spec, sink); err != nil {
		return nil, err
	}
	cmd := osexec.CommandContext(ctx, spec.Exe, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = specEnv(spec.Exe, spec.Env)
	if spec.Stdin != "" {
		cmd.Stdin = strings.NewReader(spec.Stdin)
	}
	// ctx 取消不直接杀进程：由 Stop 走 TERM→5s→KILL。Cancel 置空、WaitDelay 归零，
	// 否则 CommandContext 会在 ctx 取消后（WaitDelay 到点）自行 SIGKILL。
	cmd.Cancel = nil
	cmd.WaitDelay = 0
	setProcessGroup(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	limit := spec.MaxOutput
	if limit <= 0 {
		limit = maxCLIOutput
	}
	p := &Proc{
		kind:   spec.Kind,
		done:   make(chan exec.Result, 1),
		exited: make(chan struct{}),
		sink:   sink,
		limit:  limit,
		final:  spec.FinalText,
		diag:   spec.Diagnostic,
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p.pgid = processGroupID(cmd)

	var tail tailBuffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := scanLines(stdout, func(line []byte) { spec.Parse(line, p, p.fail) }); err != nil {
			// 单行超限时扫描器停了但不再读管道；子进程会写满管道卡住，
			// 必须抽干剩余输出，否则 cmd.Wait/wg.Wait 永远等不到 EOF。
			if errors.Is(err, bufio.ErrTooLong) {
				p.fail(errLineTooLong.Error())
			}
			drainReader(stdout)
		}
	}()
	go func() {
		defer wg.Done()
		if err := scanLines(stderr, tail.write); err != nil {
			drainReader(stderr)
		}
	}()
	go func() {
		wg.Wait()
		err := cmd.Wait()
		if spec.Done != nil {
			spec.Done(p)
		}
		res := p.result(err, tail.string())
		p.emitMu.Lock()
		p.sealed = true
		p.emitMu.Unlock()
		close(p.exited)
		p.done <- res
		close(p.done)
	}()
	go p.heartbeat(ctx, heartbeatInterval)
	go p.watchContext(ctx)
	return p, nil
}

// Emit 实现 exec.Sink：累积 EventText 并转发给调用方的 sink。
// 解析器和心跳都经过这里，事件顺序与文本累积由同一把锁保护。
func (p *Proc) Emit(e exec.Event) {
	p.emitMu.Lock()
	defer p.emitMu.Unlock()
	if p.sealed {
		return
	}
	if e.Kind == exec.EventText {
		if room := p.limit - p.text.Len(); room > 0 {
			if len(e.Text) <= room {
				p.text.WriteString(e.Text)
			} else {
				p.text.WriteString(e.Text[:room])
			}
		}
	}
	p.sink.Emit(e)
}

func (p *Proc) Done() <-chan exec.Result { return p.done }

// hasExited 报告进程是否已经收尸。发任何进程组信号前都要先查：进程退出后 PID/进程组
// 很快可以被系统复用，此时再发 SIGSTOP/SIGTERM 会误伤无关进程。
func (p *Proc) hasExited() bool {
	select {
	case <-p.exited:
		return true
	default:
		return false
	}
}

// Pause 挂起整个进程组；进程已退出时什么也不做。
func (p *Proc) Pause(context.Context) error {
	if p.hasExited() {
		return nil
	}
	return pauseGroup(p.pgid)
}

// Resume 恢复整个进程组；进程已退出时什么也不做。
func (p *Proc) Resume(context.Context) error {
	if p.hasExited() {
		return nil
	}
	return resumeGroup(p.pgid)
}

// Stop 先 SIGTERM 整个进程组，5 秒不退就 SIGKILL。重复调用安全。
func (p *Proc) Stop(ctx context.Context) error {
	// 先查 exited 再置 stopping：进程已经收尸后，这个进程组的信号可能打到被复用的 PID 上。
	if p.hasExited() {
		return nil
	}
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return nil
	}
	p.stopping = true
	p.mu.Unlock()
	err := termGroup(p.pgid)
	if errors.Is(err, ErrSignalUnsupported) {
		return p.kill()
	}
	if err != nil {
		if p.hasExited() { // 检查与发信号之间进程自己退出了
			return nil
		}
		return err
	}
	timer := time.NewTimer(stopGrace)
	defer timer.Stop()
	select {
	case <-p.exited:
		return nil
	case <-ctx.Done():
	case <-timer.C:
	}
	return p.kill()
}

// kill 容忍“进程刚好已经退出”：taskkill 对已结束的进程返回非零，
// 不应让 Stop 把一个已经结束的任务报成取消失败。
func (p *Proc) kill() error {
	if err := killGroup(p.pgid); err != nil {
		select {
		case <-p.exited:
			return nil
		default:
		}
		return err
	}
	return nil
}

func (p *Proc) fail(msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	p.mu.Lock()
	if p.errText == "" {
		p.errText = msg
	}
	p.mu.Unlock()
}

// heartbeat 每 15 秒报一次“还活着”，让 watchStalls 不会把长跑的外部任务误判为卡死。
func (p *Proc) heartbeat(ctx context.Context, interval time.Duration) {
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.exited:
			return
		case <-tk.C:
			p.Emit(exec.Event{Kind: exec.EventHeartbeat})
		}
	}
}

// watchContext 兜底：调用方取消 ctx 却没显式 Stop 时，仍然按 TERM→KILL 收尸。
func (p *Proc) watchContext(ctx context.Context) {
	select {
	case <-p.exited:
		return
	case <-ctx.Done():
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), stopGrace+2*time.Second)
	defer cancel()
	_ = p.Stop(stopCtx)
}

func (p *Proc) output() string {
	p.emitMu.Lock()
	text := strings.TrimSpace(p.text.String())
	p.emitMu.Unlock()
	if p.final != nil {
		text = strings.TrimSpace(p.final())
	}
	return text
}

func (p *Proc) result(err error, stderr string) exec.Result {
	p.mu.Lock()
	stopping, errText := p.stopping, p.errText
	p.mu.Unlock()
	out := truncate(p.output(), resultTextLimit)
	switch {
	case stopping:
		return exec.Result{Status: "cancelled", Output: out, Interrupted: true}
	case errText != "":
		return exec.Result{Status: "failed", Output: out, Error: truncate(errText, errorDetailLimit)}
	case err != nil:
		msg := strings.TrimSpace(stderr)
		if msg == "" && p.diag != nil {
			msg = strings.TrimSpace(p.diag())
		}
		if msg == "" {
			msg = err.Error()
		}
		return exec.Result{Status: "failed", Output: out, Error: truncate(msg, errorDetailLimit)}
	case out == "":
		return exec.Result{Status: "failed", Error: replyMissing(p.kind)}
	}
	return exec.Result{Status: "completed", Output: out}
}

func replyMissing(kind exec.Kind) string {
	if name := Name(kind); name != "" {
		return name + " 没有返回回复，请检查它的登录和模型配置"
	}
	return "CLI 没有返回回复，请检查它的登录和模型配置"
}

func validateSpec(spec Spec, sink exec.Sink) error {
	if spec.Exe == "" {
		return errors.New("cli: 没有指定可执行文件")
	}
	if spec.Parse == nil {
		return errors.New("cli: 没有输出解析器")
	}
	if sink == nil {
		return errors.New("cli: 没有事件 sink")
	}
	return nil
}

// specEnv 返回子进程环境：nil 表示继承进程环境；非 nil 也只剥 reeve 自身凭据。
// 厂商 Key 的去留已经由 CommandEnv 按认证方式决定，这一层再剥会把 auth=env 补进来的
// OPENAI_API_KEY 等删掉（旧路径只过滤一次）。
// npm 装的 CLI 用 `#!/usr/bin/env node` 启动，所以可执行文件所在目录要进 PATH。
func specEnv(exe string, env []string) []string {
	if env == nil {
		env = safeEnviron()
	} else {
		env = launchEnv(env)
	}
	if runtime.GOOS == "windows" || !filepath.IsAbs(exe) {
		return env
	}
	dir := filepath.Dir(exe)
	env = withPathDir(env, dir)
	if managed := managedCLIBin(); managed != "" && dir == managed {
		env = withPathDir(env, managedNodeBin())
	}
	return env
}

// launchEnv 是 Launch 层对调用方环境的最后一道过滤：只剥 reeve 自身凭据。
// 厂商 Key（OPENAI_API_KEY 等）归 CommandEnv 管，auth=env 时它们必须能到子进程。
func launchEnv(env []string) []string { return envx.Filter(env, reeveOwnEnv) }

func scanLines(r io.Reader, fn func([]byte)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), scanLineLimit)
	for sc.Scan() {
		line := sc.Bytes()
		if n := len(line); n > 0 && line[n-1] == '\r' {
			line = line[:n-1]
		}
		fn(line)
	}
	return sc.Err()
}

// drainReader 抽干管道。扫描器遇到超长行会停下且不再读取，不抽干的话子进程会写满管道
// 永久阻塞在 write 上，Wait 就永远不返回。
func drainReader(r io.Reader) { _, _ = io.Copy(io.Discard, r) }

// tailBuffer 只保留 stderr 的尾部：失败文案通常在最末尾几行。
type tailBuffer struct{ b []byte }

func (t *tailBuffer) write(line []byte) {
	t.b = append(t.b, line...)
	t.b = append(t.b, '\n')
	if len(t.b) > stderrTailLimit {
		copy(t.b, t.b[len(t.b)-stderrTailLimit:])
		t.b = t.b[:stderrTailLimit]
	}
}

func (t *tailBuffer) string() string { return strings.TrimSpace(string(t.b)) }

// truncate 把过长的文本截成「开头 + 提示 + 结尾」，避免撑爆上下文。
func truncate(s string, n int) string {
	if n <= 0 {
		n = resultTextLimit
	}
	if len(s) <= n {
		return s
	}
	head, tail := n*2/3, n/3
	return strings.ToValidUTF8(s[:head]+fmt.Sprintf("\n…[省略 %d 字节]…\n", len(s)-head-tail)+s[len(s)-tail:], "")
}

// sinkFunc 让适配器与测试可以就地构造一个 Sink。
type sinkFunc func(exec.Event)

func (f sinkFunc) Emit(e exec.Event) { f(e) }
