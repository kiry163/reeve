package commands

import (
	"context"
	"strings"
)

// Input 是传给命令处理函数的输入。
type Input struct {
	Args []string // 命令名之后的参数，已按空白切分
	Bot  BotAPI
}

// Result 是命令的执行结果。
type Result struct {
	Text string
	OK   bool
}

// BotAPI 是命令可以调用的 bot 能力集合。
type BotAPI interface {
	StopAll() int
	ClearContext() error
	ListTasks() string
	ContextInfo() string
	ListSkills() []string
	ListEnv() (instance, bot map[string]string, err error)
	SetEnv(name, value string, global bool) error
	UnsetEnv(name string, global bool) error
	SwitchModel(name string) (string, error)
	DefaultExecutor() string
	AllowedExecutors() []string
	SwitchExecutor(kind string) (string, error)
	Approve(id, decision string) error
}

// Execute 解析并执行一行文本。
//
// 文本去掉首尾空白后必须以 / 开头，否则返回 (Result{}, false)，表示这不是
// 一条命令。命令名取到第一个空白为止，大小写不敏感；参数按空白切分。
// 未知命令返回帮助文本且 Result.OK 为 false。第二个返回值只表示文本是否是
// 命令，与命令执行是否成功无关。
func (r *Registry) Execute(ctx context.Context, bot BotAPI, line string) (Result, bool) {
	text := strings.TrimSpace(line)
	if !strings.HasPrefix(text, "/") {
		return Result{}, false
	}

	fields := strings.Fields(text[1:])
	if len(fields) == 0 {
		return Result{Text: r.help()}, true
	}

	d, ok := r.Lookup(fields[0])
	if !ok {
		return Result{Text: r.help()}, true
	}

	var args []string
	if len(fields) > 1 {
		args = fields[1:]
	}
	in := Input{Args: args, Bot: bot}
	return d.Run(ctx, in), true
}
