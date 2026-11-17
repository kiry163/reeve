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
	TaskID       string
	BotID        string
	Workspace    string
	Input        string
	Instructions string
	History      []string
	Env          []string // 进程环境 + 实例级 + 调用级合并后的环境（不含 reeve 自身凭据）
	Model        string
	MaxDuration  time.Duration
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
