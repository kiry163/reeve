# Reeve M3：外部执行器 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让任务可以交给外部 CLI（codex / pi / omp）执行：每 Bot 白名单、派活前一次审批、进程组暂停/恢复/终止、流式输出、重启标中断。

**Architecture:** 新增 `internal/exec`（Runner/Handle/Sink 接口 + 注册表）与 `internal/exec/cli`（共享进程启动器 + 三个适配器）。外部任务不再走 `builtin.Agent` 的 CLI 分支，而是由 `supervisor` 直接驱动 `exec.Handle`，并把 `exec.Event` 翻译成既有 `builtin.Event`，因此客户端事件契约不变。白名单与默认执行器落到每 Bot 的 `executors.json`，`start_task` 带执行器参数并在派活前审批。

**Tech Stack:** Go 1.22（标准库，零新增依赖）；React + Vite（`web/`）；事件溯源 `stream.jsonl`。

**Spec:** `docs/superpowers/specs/2026-11-14-reeve-design.md`（§8 执行器契约/§8.1 差异表/§8.2 白名单/§10 监工/§11 审批/§16.1 端点）

## Global Constraints

- Go 1.22，**零新增依赖**（不引入 CLI 解析库、不加 npm 包）。
- 事件双写规则不变：流式文字/心跳只 `Broadcast`，不落盘；终态与工具调用落盘。外部 CLI 的**完整输出**只出现在 `task.completed` 的 `Output`，流式片段不进 `stream.jsonl`。
- CLI 输出上限：成功输出 `Truncate(out, 16000)`；失败详情 `Truncate(details, 1400)`；内部缓冲 1 MiB。
- 密钥边界：外部 CLI 拿到三层合并 env（实例级→bot 级→调用级），但 `hiddenEnv`（`REEVE_MASTER_KEY`/`REEVE_API_KEY`/`REEVE_PASSWORD`/`API_KEY`/`CHAT_API_KEY`/`XAI_API_KEY`/`OPENAI_API_KEY`…）永不下发；`{{secret:...}}` 不进 env；prompt 发送前过 `RedactString`。
- 外部任务**不自动重跑**；进程重启后一律 `paused` + `StopReason=interrupted`（沿用现有 `restart` 词汇之外的唯一新值 `interrupted`），不自动杀残留进程。
- 白名单默认 `["builtin"]`，永远包含 `builtin`；白名单外的派活**直接报错，不弹卡片**。
- 外部派活是 `exec` 风险：在**派活前**审批一次；批准后 CLI 内部命令不再逐条弹卡。
- 每个适配器源码文件头部必须有「实测记录」（版本、命令、样例行），取自本机实测输出。
- 写文件权限：`executors.json` 0600；目录 0700（复用现有的写入助手，不自己造）。

---

### Task 1: `internal/exec` 接口与注册表

**Files:**
- Create: `internal/exec/exec.go`
- Test: `internal/exec/exec_test.go`

**Interfaces:**
- Produces（后续任务全部依赖这些名字，不得改名）：
  - `exec.Kind`（string）、常量 `KindBuiltin/KindCodex/KindPi/KindOMP`
  - `exec.Request{TaskID, BotID, Workspace, Input, Instructions string; History []string; Env []string; Model string; MaxDuration time.Duration}`
  - `exec.Event{Kind EventKind; Text, Tool, Input, Output, Err string}`
  - `exec.EventKind` 常量 `EventText/EventTool/EventToolResult/EventHeartbeat`
  - `exec.Sink`（`Emit(Event)`）
  - `exec.Result{Status string; Output, Error string; Interrupted bool}`
  - `exec.Handle`（`Pause/Resume/Stop(context.Context) error`、`Done() <-chan Result`）
  - `exec.Runner`（`Kind() Kind`、`Available(context.Context) error`、`Start(context.Context, Request, Sink) (Handle, error)`）
  - `exec.NewRegistry(...Runner) *Registry`、`(*Registry) Get(Kind) (Runner, bool)`、`(*Registry) Kinds() []Kind`

- [ ] **Step 1: 写失败测试**

```go
package exec

import (
	"context"
	"errors"
	"testing"
)

type stubRunner struct{ kind Kind; available error }

func (s stubRunner) Kind() Kind { return s.kind }
func (s stubRunner) Available(context.Context) error { return s.available }
func (s stubRunner) Start(context.Context, Request, Sink) (Handle, error) {
	return nil, errors.New("not implemented")
}

func TestRegistryGetAndKinds(t *testing.T) {
	r := NewRegistry(stubRunner{kind: KindOMP}, stubRunner{kind: KindCodex})
	if _, ok := r.Get(KindOMP); !ok {
		t.Fatal("KindOMP 未注册")
	}
	kinds := r.Kinds()
	if len(kinds) != 2 || kinds[0] != KindCodex || kinds[1] != KindOMP {
		t.Fatalf("Kinds() 应按字母序返回 [codex omp]，得到 %v", kinds)
	}
	if _, ok := r.Get(KindPi); ok {
		t.Fatal("未注册的 KindPi 不应命中")
	}
}

func TestRegistryDuplicateKindPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("重复注册同一 Kind 应 panic，避免静默覆盖适配器")
		}
	}()
	NewRegistry(stubRunner{kind: KindPi}, stubRunner{kind: KindPi})
}

func TestKindIsStableString(t *testing.T) {
	// Kind 会写进事件日志与 executors.json，值不能变。
	for kind, want := range map[Kind]string{KindBuiltin: "builtin", KindCodex: "codex", KindPi: "pi", KindOMP: "omp"} {
		if string(kind) != want {
			t.Fatalf("%v 的字符串应为 %q", kind, want)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/exec`
Expected: FAIL（`undefined: NewRegistry` 等）

- [ ] **Step 3: 实现**

```go
// Package exec 定义外部执行器的契约：只有事件流，没有返回值语义。
// 内置执行器（internal/builtin）不实现这个接口，它有自己的 Agent 循环；
// 这里覆盖的是 codex / pi / omp 这类“把整机执行权交给 CLI”的执行器。
package exec

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

type Kind string

const (
	KindBuiltin Kind = "builtin"
	KindCodex   Kind = "codex"
	KindPi      Kind = "pi"
	KindOMP     Kind = "omp"
)

// Request 是一次外部执行的全部输入。History 是已渲染好的上下文字符串，
// 这样 exec 包不依赖内部消息类型。
type Request struct {
	TaskID      string
	BotID       string
	Workspace   string
	Input       string
	Instructions string
	History     []string
	Env         []string // 三层合并后的环境（不含 reeve 自身凭据）
	Model       string
	MaxDuration time.Duration
}

type EventKind string

const (
	EventText       EventKind = "text"        // 流式文字
	EventTool       EventKind = "tool"        // 工具调用开始
	EventToolResult EventKind = "tool_result" // 工具调用结果
	EventHeartbeat  EventKind = "heartbeat"   // 进程仍活着（防卡死误判）
)

type Event struct {
	Kind   EventKind
	Text   string
	Tool   string
	Input  string
	Output string
	Err    string
}

type Sink interface{ Emit(Event) }

type Result struct {
	Status      string // completed | failed | cancelled
	Output      string
	Error       string
	Interrupted bool
}

type Handle interface {
	Pause(context.Context) error
	Resume(context.Context) error
	Stop(context.Context) error
	Done() <-chan Result
}

type Runner interface {
	Kind() Kind
	Available(context.Context) error
	Start(context.Context, Request, Sink) (Handle, error)
}

// Registry 按 Kind 索引适配器。重复注册同一 Kind 直接 panic：
// 静默覆盖会让“哪个二进制在跑”变得不可解释。
type Registry struct {
	mu   sync.RWMutex
	runs map[Kind]Runner
}

func NewRegistry(runners ...Runner) *Registry {
	r := &Registry{runs: map[Kind]Runner{}}
	for _, run := range runners {
		r.Register(run)
	}
	return r
}

func (r *Registry) Register(run Runner) {
	if run == nil {
		panic("exec: nil Runner")
	}
	kind := run.Kind()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.runs[kind]; dup {
		panic(fmt.Sprintf("exec: 重复注册执行器 %q", kind))
	}
	r.runs[kind] = run
}

func (r *Registry) Get(kind Kind) (Runner, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	run, ok := r.runs[kind]
	return run, ok
}

// Kinds 返回已注册的 Kind，按字母序（界面与错误文案顺序稳定）。
func (r *Registry) Kinds() []Kind {
	r.mu.RLock()
	out := make([]Kind, 0, len(r.runs))
	for kind := range r.runs {
		out = append(out, kind)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/exec && gofmt -l internal/exec`
Expected: PASS，`gofmt` 无输出

- [ ] **Step 5: 提交**

```bash
git add internal/exec
git commit -m "feat(exec): 执行器接口与注册表"
```

---

### Task 2: `internal/exec/cli` 启动器与三个适配器（新路径，暂不接线）

> 这一步新建包并写完三个适配器与进程控制，`internal/builtin` 的旧 CLI 路径**保持不动**。Task 3 立刻删旧路径。中间会有一段时间两套实现并存，这是有意的：让每个 commit 都能编译、测试全绿。

**Files:**
- Create: `internal/exec/cli/proc.go`（进程启动、逐行扫描、进程组、暂停/恢复/终止）
- Create: `internal/exec/cli/proc_unix.go`、`internal/exec/cli/proc_windows.go`
- Create: `internal/exec/cli/omp.go`、`codex.go`、`pi.go`
- Create: `internal/exec/cli/parse_test.go`、`proc_test.go`、`fakecli_test.go`
- Modify: `internal/builtin/cli.go`（把 `cliNames`/`ValidateCLIOptions`/`DetectCLIs`/`resolveCLI`/`CommandEnv`/`piChannelEnv` 迁到 `exec/cli`，本任务先复制出导出版本供新包使用）

**Interfaces:**
- Consumes: `exec.Runner`、`exec.Request`、`exec.Sink`、`exec.Event`、`exec.Result`、`exec.Handle`
- Produces:
  - `cli.NewRegistry() *exec.Registry`（注册 codex/pi/omp 三个适配器）
  - `cli.NewCodex()/NewPi()/NewOMP() exec.Runner`
  - `cli.Launch(ctx, Spec, exec.Sink) (*Proc, error)`、`cli.Spec{Kind, Exe, Args, Stdin, Dir, Env, MaxOutput, Parse}`
  - `cli.Detect() []Status`、`cli.ValidateOptions(kind, Options) (Options, error)`、`cli.CommandEnv(processEnv, envList []string, kind, auth string) []string`、`cli.Name(kind) string`
  - `cli.Options{Model, Auth, Channel string}`（字段与 JSON tag 与现有 `builtin.CLIOptions` 完全一致：`cli_model`/`cli_auth`/`cli_channel`）

- [ ] **Step 1: 写适配器解析的失败测试（真实样本）**

```go
package cli

import (
	"reflect"
	"testing"

	"github.com/kiry163/reeve/internal/exec"
)

// 这些行取自上机实测：omp --mode=json -p "Reply with the single word: pong"
// （omp v18.11.0，2026-11-15）。改动解析器必须能重新解析这些样本。
func TestOMPParseTextDelta(t *testing.T) {
	lines := []string{
		`{"type":"session","version":3,"id":"x"}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_start","contentIndex":0}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"p"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"ong"}}`,
		`{"type":"agent_end","isTerminal":true}`,
	}
	var got []exec.Event
	sink := sinkFunc(func(e exec.Event) { got = append(got, e) })
	p := ompParser{sink: sink}
	for _, line := range lines {
		p.line([]byte(line))
	}
	want := []exec.Event{{Kind: exec.EventText, Text: "p"}, {Kind: exec.EventText, Text: "ong"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text 事件不符：%#v", got)
	}
}

func TestOMPParseToolEvents(t *testing.T) {
	lines := []string{
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_start","contentIndex":1,"name":"bash"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_delta","contentIndex":1,"delta":"{\"command\":\"echo omp-tool-ok\"}"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_end","contentIndex":1}}`,
		`{"type":"tool_execution_end","toolName":"bash","isError":false,"result":"omp-tool-ok"}`,
	}
	var got []exec.Event
	p := ompParser{sink: sinkFunc(func(e exec.Event) { got = append(got, e) })}
	for _, line := range lines {
		p.line([]byte(line))
	}
	if len(got) != 2 || got[0].Kind != exec.EventTool || got[0].Tool != "bash" || got[0].Input != `{"command":"echo omp-tool-ok"}` {
		t.Fatalf("工具调用事件不符：%#v", got)
	}
	if got[1].Kind != exec.EventToolResult || got[1].Output != "omp-tool-ok" || got[1].Err != "" {
		t.Fatalf("工具结果事件不符：%#v", got[1])
	}
}

// codex 行样本：codex-cli 0.169.0 的实测输出（本机网关未启动时只有 thread/turn/error 三类，
// agent_message 行取自同一版本的 --json 契约）。
func TestCodexParseAgentMessage(t *testing.T) {
	var got []exec.Event
	p := codexParser{sink: sinkFunc(func(e exec.Event) { got = append(got, e) })}
	p.line([]byte(`{"type":"thread.started","thread_id":"abc"}`))
	p.line([]byte(`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"pong"}}`))
	p.line([]byte(`{"type":"turn.completed"}`))
	if len(got) != 1 || got[0].Kind != exec.EventText || got[0].Text != "pong" {
		t.Fatalf("codex agent_message 解析不符：%#v", got)
	}
}

func TestCodexParseFailure(t *testing.T) {
	var errText string
	p := codexParser{sink: sinkFunc(func(exec.Event) {}), fail: func(msg string) { errText = msg }}
	p.line([]byte(`{"type":"turn.failed","error":{"message":"sandbox denied"}}`))
	if errText != "sandbox denied" {
		t.Fatalf("turn.failed 未置错：%q", errText)
	}
}

func TestPiParsePlainText(t *testing.T) {
	var got []exec.Event
	p := piParser{sink: sinkFunc(func(e exec.Event) { got = append(got, e) })}
	p.line([]byte("第一行"))
	p.line([]byte("第二行"))
	p.done()
	if len(got) != 1 || got[0].Kind != exec.EventText || got[0].Text != "第一行\n第二行" {
		t.Fatalf("纯文本适配器应整段输出一次：%#v", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/exec/cli -run 'OMP|Codex|Pi'`
Expected: FAIL（`undefined: ompParser` 等）

- [ ] **Step 3: 写解析器与进程启动器**

`internal/exec/cli/proc.go`：

```go
package cli

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/kiry163/reeve/internal/exec"
)

// Spec 描述一次 CLI 启动。Parse 按行消费输出，把 CLI 的私有格式翻成 exec.Event。
type Spec struct {
	Kind      exec.Kind
	Exe       string
	Args      []string
	Stdin     string
	Dir       string
	Env       []string
	MaxOutput int
	Parse     func(line []byte, sink exec.Sink, fail func(string))
	Done      func(sink exec.Sink) // 进程正常退出后收尾（纯文本适配器在这里 flush）
}

// Proc 是正在运行的 CLI 进程组。Pause/Resume 用信号，Stop 先 TERM 再 KILL。
type Proc struct {
	cmd     *exec.Cmd
	pgid    int
	done    chan exec.Result
	mu      sync.Mutex // 保护停止状态与 sink 写入顺序
	stopping bool
	errText string
	sink    exec.Sink
}

func Launch(ctx context.Context, spec Spec, sink exec.Sink) (*Proc, error) {
	if err := validateSpec(spec); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, spec.Exe, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	if spec.Stdin != "" {
		cmd.Stdin = strings.NewReader(spec.Stdin)
	}
	// ctx 取消时由 Stop 自己执行 TERM→KILL，避免 CommandContext 直接 SIGKILL 进程组。
	cmd.Cancel = func() error { return nil }
	cmd.WaitDelay = 3 * time.Second
	setProcessGroup(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	p := &Proc{cmd: cmd, done: make(chan exec.Result, 1), sink: sink}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p.pgid = processGroupID(cmd)
	var tail strings.Builder // stderr 只保留尾部，用于失败文案
	var out strings.Builder  // 成功输出（受 MaxOutput 限制）
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		scanLines(stdout, func(line []byte) {
			if spec.MaxOutput > 0 && out.Len() < spec.MaxOutput {
				out.Write(line)
				out.WriteByte('\n')
			}
			spec.Parse(line, p.sink, p.fail)
		})
	}()
	go func() {
		defer wg.Done()
		scanLines(stderr, func(line []byte) {
			if tail.Len() < 8192 {
				tail.Write(line)
				tail.WriteByte('\n')
			}
		})
	}()
	go func() {
		wg.Wait()
		err := cmd.Wait()
		if spec.Done != nil {
			spec.Done(p.sink)
		}
		res := p.result(err, out.String(), tail.String())
		p.done <- res
		close(p.done)
	}()
	go p.heartbeat(ctx)
	return p, nil
}

func (p *Proc) Done() <-chan exec.Result { return p.done }

func (p *Proc) result(err error, out, stderr string) exec.Result {
	p.mu.Lock()
	stopping, errText := p.stopping, p.errText
	p.mu.Unlock()
	switch {
	case stopping:
		return exec.Result{Status: "cancelled", Output: strings.TrimSpace(out)}
	case errText != "":
		return exec.Result{Status: "failed", Output: strings.TrimSpace(out), Error: errText}
	case err != nil:
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = err.Error()
		}
		return exec.Result{Status: "failed", Output: strings.TrimSpace(out), Error: truncate(msg, 1400)}
	case strings.TrimSpace(out) == "":
		return exec.Result{Status: "failed", Error: "CLI 没有返回回复，请检查它的登录和模型配置"}
	}
	return exec.Result{Status: "completed", Output: truncate(strings.TrimSpace(out), 16000)}
}

func (p *Proc) fail(msg string) {
	p.mu.Lock()
	if p.errText == "" {
		p.errText = msg
	}
	p.mu.Unlock()
}

// heartbeat 每 15 秒报一次“还活着”，让 watchStalls 不会把长跑的外部任务误判为卡死。
func (p *Proc) heartbeat(ctx context.Context) {
	tk := time.NewTicker(15 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.done:
			return
		case <-tk.C:
			p.sink.Emit(exec.Event{Kind: exec.EventHeartbeat})
		}
	}
}

func (p *Proc) Pause(ctx context.Context) error  { return signalGroup(p.pgid, pauseSignal()) }
func (p *Proc) Resume(ctx context.Context) error { return signalGroup(p.pgid, resumeSignal()) }

// Stop：SIGTERM 整个进程组，5 秒不退就 SIGKILL。重复调用安全。
func (p *Proc) Stop(ctx context.Context) error {
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return nil
	}
	p.stopping = true
	p.mu.Unlock()
	if err := signalGroup(p.pgid, termSignal()); err != nil {
		return err
	}
	select {
	case <-p.done:
		return nil
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
	}
	return signalGroup(p.pgid, killSignal())
}

func scanLines(r io.Reader, fn func([]byte)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		fn(sc.Bytes())
	}
}

// sinkFunc 让测试不用为每个用例定义类型。
type sinkFunc func(exec.Event)

func (f sinkFunc) Emit(e exec.Event) { f(e) }
```

`internal/exec/cli/proc_unix.go`：

```go
//go:build !windows

package cli

import (
	"os/exec"
	"syscall"
)

func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func processGroupID(cmd *exec.Cmd) int { return cmd.Process.Pid }

func signalGroup(pgid int, sig syscall.Signal) error {
	if pgid <= 0 {
		return nil
	}
	return syscall.Kill(-pgid, sig)
}

func pauseSignal() syscall.Signal  { return syscall.SIGSTOP }
func resumeSignal() syscall.Signal { return syscall.SIGCONT }
func termSignal() syscall.Signal   { return syscall.SIGTERM }
func killSignal() syscall.Signal   { return syscall.SIGKILL }
```

`internal/exec/cli/proc_windows.go`：

```go
//go:build windows

package cli

import (
	"os/exec"
	"strconv"
)

func setProcessGroup(cmd *exec.Cmd) {}

func processGroupID(cmd *exec.Cmd) int { return cmd.Process.Pid }

// Windows 没有 SIGSTOP/SIGCONT：暂停用 NtSuspendProcess 不可依赖，M3 直接报错，
// 由 supervisor 退回“安全点暂停”提示。
func signalGroup(int, any) error { return errSignalUnsupported }

var errSignalUnsupported = exec.ErrNotFound

func pauseSignal() string  { return "" }
func resumeSignal() string { return "" }
func termSignal() string   { return "" }
func killSignal() string   { return "" }

func killTree(pid int) error { return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run() }
```

> Windows 版需要在 `proc.go` 里用**统一的信号抽象**：把 `signalGroup(pgid int, sig any)` 改成按平台提供的 `pauseGroup/resumeGroup/termGroup/killGroup(pgid int) error`。实现者按此调整：`Proc.Pause` → `pauseGroup(p.pgid)`，Windows 下返回 `errSignalUnsupported`，`supervisor` 见到该错误就把任务标成 `paused(unsupported)` 并提示“该执行器在 Windows 上不支持进程挂起”。

`internal/exec/cli/omp.go`（含实测记录）：

```go
package cli

// 实测记录（omp v18.11.0，2026-11-15，macOS arm64）：
//
//	$ omp --mode=json -p "Reply with the single word: pong" --no-tools --no-session --no-skills --no-rules
//	{"type":"session","version":3,"id":"01a1df48-…","cwd":"/tmp"}
//	{"type":"agent_start"}
//	{"type":"message_update","assistantMessageEvent":{"type":"text_start","contentIndex":0}}
//	{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"p"}}
//	{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"ong"}}
//	{"type":"message_update","assistantMessageEvent":{"type":"text_end","contentIndex":0,"content":"pong"}}
//	{"type":"agent_end","isTerminal":true}
//
// 工具调用的行形状（同一版本，允许 bash 工具）：
//	message_update.assistantMessageEvent.type = toolcall_start/toolcall_delta/toolcall_end（name 在 start 上）
//	{"type":"tool_execution_end","toolName":"bash","isError":false,"result":"omp-tool-ok"}
//
// 启动参数：-p 非交互、--mode=json 事件流、--no-session 不落会话、--model 指定模型。
// 提示词走位置参数（argv），因此有长度上限（见 maxArgvPrompt）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kiry163/reeve/internal/exec"
)

const maxArgvPrompt = 24_000

type ompRunner struct{ look Lookup }

func NewOMP() exec.Runner { return &ompRunner{look: defaultLookup} }

func (r *ompRunner) Kind() exec.Kind { return exec.KindOMP }

func (r *ompRunner) Available(ctx context.Context) error {
	exe, err := r.look("omp")
	if err != nil {
		return errors.New("找不到 omp，请先安装并放进 PATH")
	}
	return probeVersion(ctx, exe)
}

func (r *ompRunner) Start(ctx context.Context, req exec.Request, sink exec.Sink) (exec.Handle, error) {
	exe, err := r.look("omp")
	if err != nil {
		return nil, errors.New("找不到 omp，请先安装并放进 PATH")
	}
	prompt := buildPrompt(req)
	if len(prompt) > maxArgvPrompt {
		return nil, fmt.Errorf("omp 的任务说明过长（%d 字节，上限 %d），请缩短后重试", len(prompt), maxArgvPrompt)
	}
	args := []string{"-p", "--mode=json", "--no-session", "--no-skills", "--no-rules"}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.MaxDuration > 0 {
		args = append(args, "--max-time", fmt.Sprintf("%d", int(req.MaxDuration.Seconds())))
	}
	args = append(args, prompt)
	return Launch(ctx, Spec{Kind: exec.KindOMP, Exe: exe, Args: args, Dir: req.Workspace, Env: req.Env,
		MaxOutput: 1 << 20, Parse: func(line []byte, sink exec.Sink, fail func(string)) { parseOMP(line, sink, fail) }}, sink)
}

type ompParser struct {
	sink   exec.Sink
	tools  map[int]*ompTool
	fail   func(string)
}

type ompTool struct{ name, input, result string }

func parseOMP(line []byte, sink exec.Sink, fail func(string)) {
	line = bytesTrimBOM(line)
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct{ Type, Text string } `json:"content"`
		} `json:"message"`
		AssistantMessageEvent struct {
			Type         string `json:"type"`
			ContentIndex int    `json:"contentIndex"`
			Delta        string `json:"delta"`
			Name         string `json:"name"`
			Content      string `json:"content"`
		} `json:"assistantMessageEvent"`
		ToolName string          `json:"toolName"`
		IsError  bool            `json:"isError"`
		Result   json.RawMessage `json:"result"`
		Error    json.RawMessage `json:"error"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	switch ev.Type {
	case "message_update":
		a := ev.AssistantMessageEvent
		switch a.Type {
		case "text_delta":
			if a.Delta != "" {
				sink.Emit(exec.Event{Kind: exec.EventText, Text: a.Delta})
			}
		case "toolcall_start":
			sink.Emit(exec.Event{Kind: exec.EventTool, Tool: a.Name})
		case "toolcall_delta":
			sink.Emit(exec.Event{Kind: exec.EventTool, Tool: "", Input: a.Delta})
		}
	case "tool_execution_end":
		out, errText := ompToolResult(ev.Result, ev.IsError)
		sink.Emit(exec.Event{Kind: exec.EventToolResult, Tool: ev.ToolName, Output: out, Err: errText})
	case "error":
		fail(ompErrorText(ev.Error))
	}
}

func ompToolResult(raw json.RawMessage, isErr bool) (string, string) {
	text := strings.Trim(string(raw), `"`)
	if !isErr {
		return truncate(text, 16000), ""
	}
	if text == "" {
		text = "工具执行失败"
	}
	return "", truncate(text, 1400)
}

func ompErrorText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s
	}
	var m struct{ Message string `json:"message"` }
	if json.Unmarshal(raw, &m) == nil && m.Message != "" {
		return m.Message
	}
	return "omp 报错"
}

var _ = time.Second
```

> 说明：上面为了保持可读把辅助函数拆在文件内；实现时把 `ompParser`/`ompTool` 这类不再需要的结构删掉，只保留 `parseOMP` 纯函数版本（测试用 `parseOMP` 直接喂行，避免结构体状态）。对应测试改成：
> ```go
> sink := sinkFunc(func(e exec.Event) { got = append(got, e) })
> for _, line := range lines { parseOMP([]byte(line), sink, func(msg string) { t.Fatalf("不应报错: %s", msg) }) }
> ```

`internal/exec/cli/codex.go` 与 `pi.go` 同构：

- codex：`Args = ["exec","--skip-git-repo-check","--sandbox","workspace-write","--json"]`（+`--model`）+ `"-"`，`Stdin = prompt`；解析 `item.completed`：`item.type=="agent_message"` → `EventText`（item.text），`item.type=="command_execution"` → `EventToolResult`（命令与聚合输出），`turn.failed`/`error` → `fail(message)`；`Done` 无 flush。
- pi：`Args = ["--print","--no-session"]`（+`--provider pi`/`--model`），`Stdin = prompt`；纯文本：`Parse` 把行累积进缓冲，`Done` 时一次性 `EventText`（整段），保持与现状一致的“一段式输出”。
- 三者共享 `buildPrompt(req)`：`system`（Instructions，≤4500 字符）+ `History`（每条 `role: content`，合计 ≤5200 字符）+ `Input`；超长时先截断 History 最旧的部分，并在末尾加 `（更早的对话已省略）`。

- [ ] **Step 4: 写进程控制的失败测试**

```go
//go:build !windows

package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiry163/reeve/internal/exec"
)

// 假 CLI：每 100ms 打印一行，直到被杀。
func writeLoopScript(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "loop.sh")
	script := "#!/bin/sh\nwhile true; do echo tick; sleep 0.1; done\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProcPauseResumeStop(t *testing.T) {
	script := writeLoopScript(t)
	ctx := context.Background()
	got := make(chan exec.Event, 64)
	p, err := Launch(ctx, Spec{Kind: exec.KindOMP, Exe: script, Dir: t.TempDir(), Env: os.Environ(),
		Parse: func(line []byte, sink exec.Sink, fail func(string)) {
			if strings.TrimSpace(string(line)) == "tick" {
				sink.Emit(exec.Event{Kind: exec.EventHeartbeat})
			}
		}}, sinkFunc(func(e exec.Event) { got <- e }))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := p.Pause(ctx); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	// 暂停后 400ms 内不应再有新事件
	drain(got)
	time.Sleep(400 * time.Millisecond)
	if len(got) > 2 { // 允许 1-2 条在途行
		t.Fatalf("SIGSTOP 后仍在输出：%d 条", len(got))
	}
	if err := p.Resume(ctx); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if len(got) == 0 {
		t.Fatal("SIGCONT 后没有恢复输出")
	}
	start := time.Now()
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case res := <-p.Done():
		if res.Status != "cancelled" {
			t.Fatalf("停止后状态应为 cancelled，得到 %q（err=%q）", res.Status, res.Error)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("Stop 后 8 秒仍未退出")
	}
	if d := time.Since(start); d > 7*time.Second {
		t.Fatalf("Stop 用了 %s，超过 TERM+5s+KILL 的预期", d)
	}
}

func TestProcKillsStubbornProcess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stubborn.sh")
	// 忽略 TERM，只能靠 SIGKILL 结束
	if err := os.WriteFile(path, []byte("#!/bin/sh\ntrap '' TERM\nwhile true; do sleep 0.1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := Launch(context.Background(), Spec{Kind: exec.KindOMP, Exe: path, Dir: dir, Env: os.Environ(),
		Parse: func([]byte, exec.Sink, func(string)) {}}, sinkFunc(func(exec.Event) {}))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case res := <-p.Done():
		if res.Status != "cancelled" {
			t.Fatalf("应为 cancelled，得到 %q", res.Status)
		}
	case <-time.After(9 * time.Second):
		t.Fatal("忽略 TERM 的进程未被 SIGKILL 终止")
	}
	if d := time.Since(start); d < 4*time.Second {
		t.Fatalf("Stop 太快（%s）：应先 TERM 并等 5 秒宽限", d)
	}
}

func drain(ch chan exec.Event) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/exec/cli -count=1 && go test -race ./internal/exec/cli -run Proc && gofmt -l internal/exec`
Expected: PASS（`ProcPauseResumeStop` 依赖 `tick` 脚本；若环境不允许 SIGSTOP 会被跳过——不允许跳过，失败就修）

- [ ] **Step 6: 提交**

```bash
git add internal/exec/cli
git commit -m "feat(exec): CLI 启动器与 codex/pi/omp 适配器"
```

---

### Task 3: 切到新执行路径并删掉内置 CLI 分支

**Files:**
- Modify: `internal/bot/supervisor.go`（`schedule`/`CreateTask`/`builtinTask`/`PauseTask`/`ResumeTask`/`CancelTask`/`watchStalls`/`recover`）
- Modify: `internal/bot/bot.go:190-215`（装配 `exec.Registry`）
- Modify: `internal/bot/chat.go`（`runDirectCLI`）
- Modify: `internal/bot/connections.go`（`selectedCLI` 改为读 `executors.json`，本任务先保留旧读取，Task 4 换掉）
- Delete: `internal/builtin/cli.go`、`internal/builtin/cli_step.go`、`internal/builtin/cli_gateway.go`、`internal/builtin/cli_proc*.go`、`internal/builtin/cli_options*.go`、`internal/builtin/cli_env_test.go`
- Modify: `internal/builtin/agent.go`（删 CLI 分支，`Agent.Run` 只跑内置循环）、`internal/builtin/types.go`（`Task` 删 `CLI`/`CLIOptions`）、`internal/builtin/install.go`（迁到 `exec/cli`）
- Modify: `internal/bot/server.go`（CLI 检测/安装端点改用 `exec/cli`）
- Test: `internal/bot/cli_task_test.go`（新建）

**Interfaces:**
- Consumes: `cli.NewRegistry()`、`exec.Runner`、`exec.Handle`、`exec.Event`、`exec.Result`
- Produces:
  - `Bot.execs *exec.Registry`
  - `(*Bot) cliRuns`：`map[string]*exec.Handle`（受 `b.sch.mu` 保护）
  - `(*Bot) startExternal(t TaskRec) error`、`(*Bot) emitExecEvent(taskID string, e exec.Event)`
  - 事件翻译：`exec.EventText→builtin.EvTextDelta`、`EventTool→EvToolCall`、`EventToolResult→EvToolResult`、`EventHeartbeat→EvHeartbeat`；`exec.Result.Status` → `completed|failed|cancelled` 的终态事件（复用 `builtin.Agent.finish` 同款的 `EvTaskCompleted/EvTaskFailed/EvTaskCancelled`）

- [ ] **Step 1: 写失败测试（假 CLI 全链路）**

```go
package bot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 假 omp：读 stdin 或 argv，输出两行 JSON 文本事件后退出。
func writeFakeOMP(t *testing.T) string {
	t.Helper()
	script := `#!/bin/sh
echo '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"外部"}}'
echo '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"执行完成"}}'
`
	path := filepath.Join(t.TempDir(), "omp")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExternalTaskStreamsAndCompletes(t *testing.T) {
	fake := writeFakeOMP(t)
	b := newBotWith(t, Config{ /* 见 queue_test.go 的构造方式 */ })
	b.execs = newTestRegistry(t, map[string]string{"omp": fake}) // 测试助手：PATH 前插假二进制
	id, err := b.CreateTask("跑一下", "", TaskOpts{Executor: "omp"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "completed"
	})
	rec, _ := b.store.Task(id)
	if !strings.Contains(rec.Output, "外部执行完成") {
		t.Fatalf("输出不对：%q", rec.Output)
	}
	// 流式片段只推送不落盘
	var textEvents int
	for _, e := range b.store.Events() {
		if e.Kind == "agent" {
			textEvents++
		}
	}
	if textEvents == 0 {
		t.Fatal("流式文字应通过 agent 事件推送")
	}
}
```

> `TaskOpts` 在 Task 4 才有 `Executor` 字段；本任务先用 `b.CreateTask` 的现有签名 + 设置 `b.executors().Default = "omp"` 的方式测试（即 `TestExternalTaskStreamsAndCompletes` 用默认执行器触发）。实现者按此调整，测试意图不变：外部任务走 `exec` 路径、能流式、能完成。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/bot -run ExternalTask`
Expected: FAIL（外部任务仍走 `builtin.Agent`，CLI 分支被删后无法执行）

- [ ] **Step 3: 改造 supervisor**

关键代码（`internal/bot/supervisor.go`）：

```go
// schedule 里对执行器分流
for _, t := range b.store.Queue() {
	if b.sch.launched[t.ID] {
		continue
	}
	if t.CLI != "" {
		if _, running := b.cliRun(t.ID); running {
			continue
		}
		if !b.slots.acquire(b) {
			return
		}
		b.sch.launched[t.ID] = true
		b.runBackground(func() { b.startExternal(t) })
		continue
	}
	// …原内置路径不变
}

func (b *Bot) cliRun(id string) (*exec.Handle, bool) {
	b.sch.mu.Lock()
	defer b.sch.mu.Unlock()
	h, ok := b.cliRuns[id]
	return h, ok
}

func (b *Bot) startExternal(t TaskRec) {
	runner, ok := b.execs.Get(exec.Kind(t.CLI))
	if !ok {
		b.failTask(t.ID, fmt.Sprintf("这个 Bot 没有配置执行器 %s", t.CLI))
		return
	}
	ctx, cancel := context.WithCancel(b.ctx)
	req := exec.Request{TaskID: t.ID, BotID: b.botProfile().ID, Workspace: b.cfg.Workspace,
		Input: t.Input, Instructions: b.instructionsFor(t), History: renderHistory(t.History),
		Env: b.TaskEnv(), Model: b.cliOptions(t.CLI).Model, MaxDuration: b.cfg.TaskMaxTime}
	h, err := runner.Start(ctx, req, execSink{bot: b, taskID: t.ID})
	if err != nil {
		cancel()
		b.failTask(t.ID, err.Error())
		return
	}
	b.sch.mu.Lock()
	b.cliRuns[t.ID] = &cliRun{handle: h, cancel: cancel}
	b.sch.mu.Unlock()
	b.store.Append("task.updated", TaskRec{ID: t.ID, Status: "running"}, false)
	go func() {
		res := <-h.Done()
		cancel()
		b.sch.mu.Lock()
		delete(b.cliRuns, t.ID)
		b.sch.mu.Unlock()
		b.slots.release()
		b.finishExternal(t.ID, res)
	}()
}

func (b *Bot) finishExternal(id string, res exec.Result) {
	switch res.Status {
	case "completed":
		b.store.Append("agent", builtin.Event{TaskID: id, Type: builtin.EvTaskCompleted, Time: time.Now(),
			Data: builtin.Result{Status: builtin.StatusCompleted, Output: res.Output}}, false)
	case "cancelled":
		b.store.Append("agent", builtin.Event{TaskID: id, Type: builtin.EvTaskCancelled, Time: time.Now(),
			Data: builtin.Result{Status: builtin.StatusCancelled, Output: res.Output}}, false)
	default:
		b.store.Append("agent", builtin.Event{TaskID: id, Type: builtin.EvTaskFailed, Time: time.Now(),
			Data: builtin.Result{Status: builtin.StatusFailed, Output: res.Output, Error: res.Error}}, false)
	}
	b.onAgentEvent(builtin.Event{TaskID: id, Type: builtin.EvTaskCompleted}) // 让终态处理只走一处
}
```

> 实现注意：`onAgentEvent` 里终态分支靠 `e.Terminal()` 与 `b.store.Task(...)` 的最终状态决定后续动作，所以**先 append 终态事件、再调用 `onAgentEvent` 会被重复处理**。正确做法是把终态事件 append 到 store 之后直接调用 `b.landed(id)` + `b.report(id)` 的既有路径：抽一个 `b.terminalTask(id, res)` 供内置与外部共用，内置路径的 `onAgentEvent` 改为调用它。实现者必须保证：任一任务只走一次终态处理（`task.reported` 幂等）。

外部任务的暂停/恢复/停止：

```go
func (b *Bot) cliControl(id string, f func(h exec.Handle) error) (bool, error) {
	b.sch.mu.Lock()
	run := b.cliRuns[id]
	b.sch.mu.Unlock()
	if run == nil {
		return false, nil
	}
	return true, f(*run.handle)
}
```

- `PauseTask`：先走原有状态变更；若 `cliControl` 命中且 `Pause` 成功 → `task.updated{paused, stop_reason: user}` + `evTaskPaused`。
- `ResumeTask`：`Resume`（SIGCONT）→ `task.updated{running}`。
- `CancelTask`：`Stop`（TERM→5s→KILL）→ 终态 `cancelled`.

- [ ] **Step 4: 删旧路径并迁移调用方**

```bash
git rm internal/builtin/cli.go internal/builtin/cli_step.go internal/builtin/cli_gateway.go \
       internal/builtin/cli_proc_windows.go internal/builtin/cli_proc_other.go \
       internal/builtin/cli_options_test.go internal/builtin/cli_resolve_test.go internal/builtin/cli_env_test.go
grep -rn "builtin.CLIOptions\|builtin.DetectCLIs\|builtin.RunCLI\|CLITool\|supportedCLI" internal cmd | cat
```

逐一改成 `exec/cli` 的对应符号；`builtin.Task` 删掉 `CLI`/`CLIOptions`，`Agent.Run` 删掉 `if t.CLI != ""` 分支与 `runCLIStep` 调用。

- [ ] **Step 5: 全量验证**

Run: `go build ./... && go vet ./... && go test -count=1 ./... && gofmt -l cmd internal`
Expected: 全绿（遗留的 `connections_test.go` 里 pi 用例迁到 `internal/exec/cli/pi_test.go`，用同一套假 CLI 夹具）

- [ ] **Step 6: 提交**

```bash
git add -A
git commit -m "refactor(exec): 外部任务改由 exec.Handle 驱动，删除内置 CLI 分支"
```

---

### Task 4: 白名单、默认执行器与 `/agent`

**Files:**
- Create: `internal/bot/executors.go`
- Create: `internal/bot/executors_test.go`
- Modify: `internal/bot/connections.go`（迁移旧 `agent-connection.json`，之后只读新文件）
- Modify: `internal/bot/server.go`（`GET/PUT /api/bots/{id}/executors`）
- Modify: `internal/bot/commands.go`、`internal/commands/builtin.go`（`/agent`）
- Modify: `web/src/api.ts`、`web/src/components/BotModelSettings.tsx`（本任务只加 API 与最小下拉；完整面板在 Task 9）

**Interfaces:**
- Consumes: `cli.Options`、`exec.Registry.Kinds()`、`cli.ValidateOptions`
- Produces:
  - `type Executors struct { Allowed []string `json:"allowed"`; Default string `json:"default"`; CLIs map[string]cli.Options `json:"clis,omitempty"` }`
  - `(*Bot) Executors() Executors`、`(*Bot) SaveExecutors(Executors) error`（校验 + 0600 落盘 + 审计事件 `executors.updated`）
  - `(*Bot) executorAllowed(kind string) bool`
  - HTTP：`GET/PUT /api/bots/{id}/executors`
  - `/agent`：`/agent` 显示默认与白名单；`/agent list` 列出；`/agent <kind>` 切换默认（必须在白名单内）

- [ ] **Step 1: 写失败测试**

```go
func TestSaveExecutorsRejectsUnknownKind(t *testing.T) {
	b := newBotWith(t, Config{})
	err := b.SaveExecutors(Executors{Allowed: []string{"builtin", "nope"}, Default: "builtin"})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("未知执行器应被拒绝并点名，得到 %v", err)
	}
}

func TestSaveExecutorsRequiresBuiltinAndDefaultInAllowed(t *testing.T) {
	b := newBotWith(t, Config{})
	if err := b.SaveExecutors(Executors{Allowed: []string{"codex"}, Default: "codex"}); err == nil {
		t.Fatal("白名单必须包含 builtin")
	}
	if err := b.SaveExecutors(Executors{Allowed: []string{"builtin", "codex"}, Default: "omp"}); err == nil {
		t.Fatal("默认执行器必须在白名单内")
	}
}

func TestExecutorsMigrationFromAgentConnection(t *testing.T) {
	b := newBotWith(t, Config{})
	// 旧文件：选过 pi，认证走 channel
	writeLegacyConnection(t, b, agentConnection{CLI: "pi", CLIOptions: cli.Options{Auth: "channel", Model: "x"}})
	got := b.Executors()
	if !slices.Equal(got.Allowed, []string{"builtin", "pi"}) || got.Default != "pi" {
		t.Fatalf("迁移结果不对：%+v", got)
	}
	if got.CLIs["pi"].Model != "x" {
		t.Fatalf("应保留旧 CLI 选项：%+v", got.CLIs)
	}
	if _, err := os.Stat(filepath.Join(b.dataDir(), "agent-connection.json")); !os.IsNotExist(err) {
		t.Fatal("迁移后旧文件应删除")
	}
}

func TestAgentCommandSwitchesDefaultWithinWhitelist(t *testing.T) {
	b := newBotWith(t, Config{})
	if err := b.SaveExecutors(Executors{Allowed: []string{"builtin", "codex"}, Default: "builtin"}); err != nil {
		t.Fatal(err)
	}
	res, ok := b.CommandLine(context.Background(), "/agent codex")
	if !ok || !res.OK || !strings.Contains(res.Text, "codex") {
		t.Fatalf("/agent codex 应成功：%+v", res)
	}
	res, _ = b.CommandLine(context.Background(), "/agent omp")
	if res.OK || !strings.Contains(res.Text, "白名单") {
		t.Fatalf("白名单外应被拒绝并提示白名单：%+v", res)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/bot -run 'Executors|AgentCommand'`
Expected: FAIL（`undefined: Executors`）

- [ ] **Step 3: 实现 `internal/bot/executors.go`**

```go
// 每个 Bot 的执行器配置：白名单 + 默认执行器 + 每个外部 CLI 的选项。
// 落盘到 <botDir>/executors.json（0600）。旧的 agent-connection.json 在首次读取时迁移。
type Executors struct {
	Allowed []string               `json:"allowed"`
	Default string                 `json:"default"`
	CLIs    map[string]cli.Options `json:"clis,omitempty"`
}

func defaultExecutors() Executors {
	return Executors{Allowed: []string{string(exec.KindBuiltin)}, Default: string(exec.KindBuiltin)}
}

// SaveExecutors 校验并落盘；白名单必须包含 builtin，Default 必须在 Allowed 内，
// 每个 kind 必须在注册表里（未注册的适配器不允许出现在白名单）。
func (b *Bot) SaveExecutors(in Executors) error {
	out := Executors{Allowed: nil, Default: in.Default, CLIs: map[string]cli.Options{}}
	seen := map[string]bool{}
	for _, kind := range in.Allowed {
		kind = strings.TrimSpace(kind)
		if kind == "" || seen[kind] {
			continue
		}
		if _, ok := b.execs.Get(exec.Kind(kind)); !ok {
			return fmt.Errorf("未知执行器 %q（可用：%s）", kind, joinKinds(b.execs.Kinds()))
		}
		seen[kind] = true
		out.Allowed = append(out.Allowed, kind)
	}
	if !seen[string(exec.KindBuiltin)] {
		return errors.New("白名单必须包含 builtin")
	}
	if out.Default == "" {
		out.Default = string(exec.KindBuiltin)
	}
	if !seen[out.Default] {
		return fmt.Errorf("默认执行器 %q 不在白名单里", out.Default)
	}
	for kind, opts := range in.CLIs {
		if !seen[kind] {
			continue // 白名单外的选项不保存，避免灰数据
		}
		v, err := cli.ValidateOptions(kind, opts)
		if err != nil {
			return fmt.Errorf("执行器 %s 的设置无效：%w", kind, err)
		}
		out.CLIs[kind] = v
	}
	if err := writeJSONFile(b.executorsPath(), out, 0o600); err != nil {
		return err
	}
	b.store.Append("executors.updated", map[string]any{"allowed": out.Allowed, "default": out.Default}, false)
	return nil
}
```

- [ ] **Step 4: HTTP 端点 + `/agent` 命令**

```go
mux.HandleFunc("GET /api/executors", func(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, b.Executors(), nil)
})
mux.HandleFunc("PUT /api/executors", func(w http.ResponseWriter, r *http.Request) {
	var in Executors
	if !decodeJSON(w, r, &in, maxJSONBody) {
		return
	}
	err := b.SaveExecutors(in)
	writeJSON(w, b.Executors(), err)
})
```

`/agent` 命令（`internal/commands/builtin.go`）：

```go
register("agent", []string{"ex"}, "查看/切换默认执行器", "/agent [list|<kind>]", func(ctx context.Context, api BotAPI) Result {
	kind := strings.TrimSpace(strings.TrimPrefix(api.CommandArgs(), "agent"))
	if kind == "" {
		cur := api.DefaultExecutor()
		return Result{Text: fmt.Sprintf("默认执行器：%s\n白名单：%s", cur, strings.Join(api.AllowedExecutors(), "、")), OK: true}
	}
	if kind == "list" {
		return Result{Text: "白名单：" + strings.Join(api.AllowedExecutors(), "、"), OK: true}
	}
	text, err := api.SwitchExecutor(kind)
	return Result{Text: text, OK: err == nil}
})
```

> `commands.BotAPI` 增加三个方法：`DefaultExecutor() string`、`AllowedExecutors() []string`、`SwitchExecutor(kind string) (string, error)`；`Bot` 实现：白名单外返回错误「X 不在白名单里，当前可用：…；先用设置页把它加入白名单」。

- [ ] **Step 5: 跑测试**

Run: `go test ./internal/bot ./internal/commands -count=1 && gofmt -l cmd internal`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add internal web/src/api.ts web/src/components/BotModelSettings.tsx
git commit -m "feat(exec): 每 Bot 执行器白名单、默认执行器与设置端点"
```

---

### Task 5: 派活前审批 + 执行器参数

**Files:**
- Modify: `internal/bot/chat.go`（`start_task`/`queue_task` schema 与风险函数）
- Modify: `internal/bot/chattools.go`（`chatWrite` 复用为「派活审批」路径）
- Modify: `internal/bot/supervisor.go`（`TaskOpts.Executor`、`createTask` 记录执行器）
- Modify: `internal/bot/store.go`（`TaskRec.Executor`）
- Test: `internal/bot/executor_approval_test.go`

**Interfaces:**
- Consumes: `(*Bot) executorAllowed`、`(*Bot) chatWrite`、`builtin.RiskExec`
- Produces:
  - `TaskOpts{ID, Hold, ScheduleID string; Executor string}`（`Executor` 空 = 当前默认执行器，仅 `CreateTask` 补默认值）
  - `TaskRec.Executor string`（空 = builtin）；事件 `task.created` 里带 `executor`
  - `start_task`/`queue_task` 的 JSON schema 增加 `executor`（enum 为白名单，描述里写“省略=默认执行器”）

- [ ] **Step 1: 写失败测试**

```go
func TestDispatchExternalRequiresApproval(t *testing.T) {
	b := newBotWith(t, Config{})
	_ = b.SaveExecutors(Executors{Allowed: []string{"builtin", "omp"}, Default: "builtin"})
	// 聊天层调用 start_task，带 executor=omp
	out := runChatTool(t, b, "start_task", `{"task":"跑测试","executor":"omp"}`)
	if !strings.Contains(out, "需要用户确认") {
		t.Fatalf("外部派活应要求确认，得到 %q", out)
	}
	if n := len(b.store.Approvals()); n != 1 {
		t.Fatalf("应产生一条审批，得到 %d", n)
	}
	if n := len(b.store.Tasks()); n != 0 {
		t.Fatalf("审批前不应创建任务，得到 %d 个", n)
	}
	// 批准后任务才创建，且执行器写进记录
	ap := b.store.Approvals()[0]
	if err := b.Decide(ap.ID, "allow"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(b.store.Tasks()) == 1 })
	rec := b.store.Tasks()[0]
	if rec.Executor != "omp" {
		t.Fatalf("任务执行器应为 omp，得到 %q", rec.Executor)
	}
}

func TestDispatchOutsideWhitelistRejectsWithoutCard(t *testing.T) {
	b := newBotWith(t, Config{})
	_ = b.SaveExecutors(Executors{Allowed: []string{"builtin"}, Default: "builtin"})
	out := runChatTool(t, b, "start_task", `{"task":"跑测试","executor":"omp"}`)
	if !strings.Contains(out, "白名单") {
		t.Fatalf("白名单外应直接报错，得到 %q", out)
	}
	if n := len(b.store.Approvals()); n != 0 {
		t.Fatalf("白名单外不应弹卡片，得到 %d 条审批", n)
	}
}

func TestDispatchBuiltinNeedsNoApproval(t *testing.T) {
	b := newBotWith(t, Config{})
	out := runChatTool(t, b, "start_task", `{"task":"跑测试"}`)
	if strings.Contains(out, "需要用户确认") {
		t.Fatalf("内置派活不该要审批：%q", out)
	}
	if n := len(b.store.Approvals()); n != 0 {
		t.Fatalf("内置派活不应产生审批，得到 %d", n)
	}
}

func TestNeverPolicyDeniesExternalDispatch(t *testing.T) {
	b := newBotWith(t, Config{ChatPolicy: builtin.PolicyNever})
	_ = b.SaveExecutors(Executors{Allowed: []string{"builtin", "omp"}, Default: "builtin"})
	out := runChatTool(t, b, "start_task", `{"task":"跑测试","executor":"omp"}`)
	if !strings.Contains(out, "没有批准") && !strings.Contains(out, "拒绝") {
		t.Fatalf("never 策略下应直接拒绝：%q", out)
	}
	if n := len(b.store.Tasks()); n != 0 {
		t.Fatalf("被拒绝的派活不该建任务，得到 %d", n)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/bot -run Dispatch`
Expected: FAIL（`executor` 参数被忽略/无审批）

- [ ] **Step 3: 实现**

`start_task` 的工具定义改为：

```go
{
	Name: "start_task",
	Desc: "派一个任务给执行器。省略 executor 时用默认执行器；外部执行器（codex/pi/omp）会在派活前请用户批准一次。",
	Schema: json.RawMessage(`{"type":"object","properties":{
		"task":{"type":"string","description":"任务原文"},
		"title":{"type":"string","description":"可选短标题"},
		"executor":{"type":"string","enum":[...],"description":"执行器；省略=默认"}
	},"required":["task"]}`),
	Risk: func(t builtin.Task, name string, args json.RawMessage) builtin.RiskLevel {
		if executorFromArgs(args) != "" {
			return builtin.RiskExec
		}
		return builtin.RiskRead
	},
}
```

`enum` 由白名单动态生成（`chatTools()` 构建时读取 `b.Executors().Allowed`）。

派活执行体改为 `dispatch`：

```go
func (b *Bot) dispatch(args json.RawMessage, hold bool) (string, error) {
	var a struct{ Task, Title, Executor string }
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if a.Executor != "" && !b.executorAllowed(a.Executor) {
		return fmt.Sprintf("执行器 %s 不在这个 Bot 的白名单里（可用：%s）。换一个可用执行器，或告诉用户去设置里加白名单。",
			a.Executor, strings.Join(b.Executors().Allowed, "、")), nil
	}
	o := TaskOpts{Hold: hold, Executor: a.Executor}
	if a.Executor == "" {
		o.Executor = b.Executors().Default
	}
	if o.Executor != "" && o.Executor != string(exec.KindBuiltin) {
		id, err := b.createTaskDeferred(a.Task, a.Title, o) // 只有审批通过后才写 task.created
		return id, err
	}
	id, err := b.CreateTask(a.Task, a.Title, o)
	return "已派出任务 " + id, err
}
```

> 关键点：外部派活走 `chatWrite` 的“先审批、后执行”路径。为避免“审批前就把任务写进日志”，把 `createTask` 拆成 `createTaskDeferred`（只生成参数、不 append）与 `commitTask`（append `task.created`）；`chatWrite` 的 `execWrite` 回调里调用 `commitTask`。这样 `TestDispatchExternalRequiresApproval` 里“审批前 0 个任务”才成立。绝不要用“先建任务、任务里再等审批”的实现——那会违反 spec §8.1「派活前一次」。

- [ ] **Step 4: 去掉执行时审批**

`exec/cli` 不打审批：白名单 + 派活审批已经覆盖。删除 `cli` 启动前的 `Guard.Check`；保留 prompt 的 `RedactString`（`bot` 层注入 `Redact` 函数）。

- [ ] **Step 5: 跑测试**

Run: `go test ./internal/bot -count=1 && go test -count=1 ./... && gofmt -l cmd internal`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add -A
git commit -m "feat(exec): 派活前审批与执行器参数，白名单外直接拒绝"
```

---

### Task 6: 进程控制语义（暂停/恢复/停止/重启）

**Files:**
- Modify: `internal/bot/supervisor.go`（`PauseTask`/`ResumeTask`/`CancelTask`/`watchStalls`/`recover`）
- Modify: `internal/bot/store.go`（`TaskRec.PID`/`PGID` 仅内存；`StopReason` 新增 `interrupted`）
- Test: `internal/bot/cli_control_test.go`

**Interfaces:**
- Consumes: `exec.Handle`、`exec.Proc`
- Produces: 外部任务的 `paused(user|chat|stall)` / `paused(interrupted)` / `cancelled(user)`

- [ ] **Step 1: 写失败测试**

```go
func TestPauseExternalTaskSendsSIGSTOP(t *testing.T) {
	b, id := startSlowExternalTask(t, 10*time.Second) // 假 CLI：打印 tick 直到被杀
	if _, err := b.PauseTask(id, "user"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		rec, _ := b.store.Task(id)
		return rec.Status == "paused"
	})
	before := b.store.TaskActivity(id)
	time.Sleep(600 * time.Millisecond)
	if b.store.TaskActivity(id) != before {
		t.Fatal("SIGSTOP 后仍有活动事件")
	}
	if _, err := b.ResumeTask(id, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return b.store.TaskActivity(id) > before })
}

func TestCancelExternalTaskTerminatesGroup(t *testing.T) {
	b, id := startSlowExternalTask(t, 10*time.Second)
	if err := b.CancelTask(id, "user"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 9*time.Second, func() bool {
		rec, _ := b.store.Task(id)
		return rec.Status == "cancelled" && rec.StopReason == "user"
	})
	if n := b.slots.running(); n != 0 {
		t.Fatalf("外部任务结束后应释放并发槽，仍有 %d 个在用", n)
	}
}

func TestRecoverMarksExternalTaskInterrupted(t *testing.T) {
	dir := t.TempDir()
	// 直接构造日志：一条运行中的外部任务
	b := newBotAt(t, dir, Config{})
	b.store.Append("task.created", TaskRec{ID: "task_x", Title: "跑", Input: "跑", Executor: "omp", Status: "running"}, false)
	b2 := newBotAt(t, dir, Config{}) // 重启
	rec, ok := b2.store.Task("task_x")
	if !ok {
		t.Fatal("任务丢失")
	}
	if rec.Status != "paused" || rec.StopReason != "interrupted" {
		t.Fatalf("重启后应为 paused/interrupted，得到 %s/%s", rec.Status, rec.StopReason)
	}
}
```

- [ ] **Step 2: 实现要点**

```go
// PauseTask：外部任务发 SIGSTOP
if detected, err := b.cliControl(id, func(h exec.Handle) error { return h.Pause(b.ctx) }); detected {
	if err != nil {
		return "", fmt.Errorf("暂停失败：%w", err)
	}
	b.store.Append("task.updated", TaskRec{ID: id, Status: "paused", StopReason: by}, false)
	b.store.Append("agent", builtin.Event{TaskID: id, Type: builtin.EvTaskPaused, Time: time.Now()}, false)
	return "已暂停", nil
}
```

`watchStalls` 现在**包含**外部任务：`t.Awaiting == 0 && time.Since(t.LastActivity) > StallTimeout` 时（`Proc` 每 15 秒心跳会 `Touch`）→ `handle.Pause()` + `task.note` + `task.pausing{stall}`。`recover()`：

```go
for _, t := range b.store.Tasks() {
	if t.Status == "running" || t.Status == "queued" || t.Pausing {
		reason := "restart"
		if t.Executor != "" && t.Executor != string(exec.KindBuiltin) {
			reason = "interrupted" // spec §8.1：外部执行器重启后标中断，不自动重跑
		}
		b.store.Append("task.updated", TaskRec{ID: t.ID, Status: "paused", StopReason: reason}, false)
	}
}
```

`notify` 文案区分：中断用「服务重启，外部执行器任务「X」已中断（不会自动重跑）；需要的话重新派一次。」

> 最终实现：外部卡死检测用 `LastProgress + ExternalStallTimeout`（心跳只刷新 `LastActivity`，不算进展），见 §10。

- [ ] **Step 3: 跑测试**

Run: `go test ./internal/bot -run 'Pause|Cancel|Recover' -count=1 && go test -race ./internal/bot -run 'External|Pause' && gofmt -l cmd internal`
Expected: PASS

- [ ] **Step 4: 提交**

```bash
git add -A
git commit -m "feat(exec): 进程组暂停/恢复/终止与重启中断标记"
```

---

### Task 7: 可用性探测与适配器实测记录

**Files:**
- Modify: `internal/exec/cli/omp.go`、`codex.go`、`pi.go`（`Available`：存在 + `--version` + 版本解析）
- Modify: `internal/exec/cli/detect.go`（`Status{ID, Name, Path, Version, Available, Note}`）
- Modify: `internal/bot/server.go`（`GET /api/clis` 返回版本与可用性）
- Test: `internal/exec/cli/available_test.go`

**Interfaces:**
- Produces: `cli.Status`（`ID/Name/Path/Version/Available/Note`）、`cli.Detect(ctx) []Status`

- [ ] **Step 1: 测试**

```go
func TestAvailableParsesVersions(t *testing.T) {
	cases := []struct{ out, want string }{
		{"codex-cli 0.169.0", "0.169.0"},
		{"omp v18.11.0\n", "18.11.0"},
		{"0.12.3", "0.12.3"},
	}
	for _, c := range cases {
		if got := parseVersion(c.out); got != c.want {
			t.Fatalf("parseVersion(%q) = %q，应为 %q", c.out, got, c.want)
		}
	}
}

func TestAvailableReportsMissingBinary(t *testing.T) {
	r := NewPi().(*piRunner)
	r.look = func(string) (string, error) { return "", exec.ErrNotFound }
	if err := r.Available(context.Background()); err == nil || !strings.Contains(err.Error(), "找不到 pi") {
		t.Fatalf("缺少二进制应报错，得到 %v", err)
	}
}
```

- [ ] **Step 2: 实现**

```go
var versionPattern = regexp.MustCompile(`(\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.]+)?)`)

func parseVersion(out string) string {
	if m := versionPattern.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// probeVersion 跑 `<exe> --version`（5 秒超时）并解析版本号；
// 命令失败但能解析出版本时仍然算可用。
func probeVersion(ctx context.Context, exe string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, exe, "--version").CombinedOutput()
	if parseVersion(string(out)) == "" {
		return fmt.Errorf("%s --version 没有输出版本号，可能装坏了", filepath.Base(exe))
	}
	return nil
}
```

每个适配器文件头部补「实测记录」（版本、命令、样例行），三份都要有。`codex` 的记录必须包含这段**真实失败样本**（codex-cli 0.169.0，2026-11-15，本机上游网关未启动）：

```
$ codex exec --json --skip-git-repo-check --sandbox read-only "Reply with the single word pong"
{"type":"thread.started","thread_id":"01a1df48-6bc6-7bb2-8999-c87233b8ac41"}
{"type":"item.completed","item":{"id":"item_0","type":"error","message":"Codex is ignoring 1 unrecognized configuration setting. …"}}
{"type":"turn.started"}
{"type":"error","message":"Reconnecting... 1/5 (unexpected status 502 Bad Gateway: …)"}
{"type":"turn.failed","error":{"message":"unexpected status 502 Bad Gateway: Unknown error, url: http://127.0.0.1:8100/responses"}}
```

即：`turn.failed.error.message` 是失败原因；`item.completed` 且 `item.type=="error"` 的解释性告警**不算失败**；`agent_message` 行未在本机观测到（网关不可用），解析契约取自 `--json` 文档化字段，冒烟任务里标注“未覆盖”。

- [ ] **Step 3: 跑测试 + 提交**

Run: `go test ./internal/exec/cli -count=1 && gofmt -l internal`

```bash
git add -A
git commit -m "feat(exec): 执行器可用性与版本探测"
```

---

### Task 8: 网页（执行器面板 + 任务执行器标识）

**Files:**
- Modify: `web/src/api.ts`（`executors` 类型与调用；`clis` 带版本）
- Modify: `web/src/components/BotModelSettings.tsx`（白名单多选、默认执行器单选、每 CLI 选项、危险提示）
- Modify: `web/src/components/TaskDock.tsx` 或任务卡片组件（显示执行器徽标）
- Test: `cd web && npm run build` + 手工清单

- [ ] **Step 1: `api.ts`**

```ts
export type ExecutorKind = 'builtin' | 'codex' | 'pi' | 'omp';
export type Executors = { allowed: ExecutorKind[]; default: ExecutorKind; clis?: Record<string, {cli_model?: string; cli_auth?: string; cli_channel?: string}> };
// settingsApi 增加 executors(): Promise<Executors> / saveExecutors(next: Executors): Promise<Executors>
// clis(): Promise<{id: string; name: string; path: string; version: string; available: boolean; note?: string; installable: boolean}[]>
```

- [ ] **Step 2: 面板**

- 勾选 `builtin`（不可取消，禁用态说明“内置执行器始终可用”）。
- 勾选外部执行器时显示警告条：「外部 CLI 等于把整台机器的执行权交给它；只勾选你信任的 CLI」。
- 每个已勾选的外部执行器展示版本与登录要求（来自 `clis()`）；`available=false` 时显示 `note`。
- 默认执行器用单选，只列白名单内的项。
- 保存失败（400）时把后端文案原样显示在面板里。

- [ ] **Step 3: 任务卡片徽标**

任务卡片标题旁显示执行器：`builtin` 不显示，外部显示 `codex`/`pi`/`omp` 小徽标（`title` 说明“由外部 CLI 执行，内部命令不受 reeve 审批”）。

- [ ] **Step 4: 构建与手工验证**

Run: `cd web && npm run build`
手工（写进报告，真实浏览器）：
1. 设置页勾选 omp → 保存 → 刷新仍在；
2. 默认改成 omp → 发「跑一下 pwd」→ 任务卡片有 omp 徽标、有流式文字、最终输出落在结果里；
3. 取消勾选 builtin 被阻止并提示；
4. 把默认设成不在白名单的项被后端 400 拒绝并在面板显示。

- [ ] **Step 5: 提交**

```bash
git add web
git commit -m "feat(web): 执行器白名单面板与任务执行器标识"
```

---

### Task 9: M3 冒烟与文档

**Files:**
- Create: `docs/smoke-m3.md`
- Modify: `docs/superpowers/specs/2026-11-14-reeve-design.md`（§8 执行器实现状态：白名单/审批/进程控制已落地，`Available` 语义、Windows 无 SIGSTOP 的限制）

- [ ] **Step 1: 全量验证**

```bash
go build ./... && go vet ./... && go test -count=1 ./... && go test -race ./internal/...
cd web && npm run build
```

- [ ] **Step 2: 真实冒烟（写入文档，每条要有真实输出）**

1. 白名单外派活：`/agent omp`（未加入白名单）→ 拒绝并提示白名单；聊天层 `start_task(executor=omp)` → 报错，无审批卡片。
2. 派活前审批：加入白名单后 `start_task(executor=omp)` → 一张审批卡；批准前 `GET /api/bots/main/tasks` 为空；批准后任务创建并执行。
3. 真实 omp：任务 `pwd && echo omp-ok` → 流式文字出现、输出含 `omp-ok`；执行器徽标为 omp。
4. 真实 codex：若本机 codex 的上游可用则跑一次最小任务；不可用则记录 `codex exec --json` 的失败输出（502）并写“未覆盖，需网关可用”。
5. 进程控制：用一个慢任务（`sleep 30`）→ 暂停后 `ps -o stat=` 显示 `T`；恢复后回到 `S+`；取消后 5 秒内退出，任务 `cancelled/user`。
6. 重启：任务运行中 `kill -TERM` reeve → 重启 → 该任务 `paused/interrupted`，没有自动重跑，通知文案正确。
7. 并发槽：外部任务结束后 `slots` 归零（连续派 4 个任务验证第 4 个排队）。

- [ ] **Step 3: 写文档并提交**

```bash
git add docs
git commit -m "docs: M3 冒烟记录与执行器实现状态"
```

---

## 自审记录

- **Spec 覆盖**：§8 接口（Task 1）、§8.1 差异表（Task 2/5/6：审批时机、暂停恢复、停止、重启、凭证、env）、§8.2 白名单（Task 4）、§10 监工并发/暂停/卡死/超时/重启（Task 3/6）、§11.3 外部派活 = exec（Task 5）、§16.1 端点（Task 4）、实测记录（Task 7）。
- **明确不做（写进 spec 已知边界）**：Windows 上的 SIGSTOP/SIGCONT（返回 `errSignalUnsupported`）；孤儿进程自动清理（只标 interrupted）；CLI 输出逐 token 的思考流（omp 只取 text/tool 事件）。
- **类型一致性**：`exec.Request/Event/Result/Handle`、`cli.Options/Status/CommandEnv`、`Executors/Allowed/Default/CLIs`、`TaskOpts.Executor`、`TaskRec.Executor` 在全部任务里同名同型。
- **遗留风险**：Task 2 与 Task 3 之间并存两套 CLI 实现（一个 commit 内没有对外行为变化，Task 3 立即删除旧路径）。
