package commands

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

type stubBot struct{}

func (stubBot) StopAll() int         { return 0 }
func (stubBot) ClearContext() error  { return nil }
func (stubBot) ListTasks() string    { return "" }
func (stubBot) ContextInfo() string  { return "" }
func (stubBot) ListSkills() []string { return nil }
func (stubBot) ListEnv() (map[string]string, error) {
	return nil, nil
}
func (stubBot) SetEnv(string, string) error           { return nil }
func (stubBot) UnsetEnv(string) error                 { return nil }
func (stubBot) SwitchModel(string) (string, error)    { return "", nil }
func (stubBot) DefaultExecutor() string               { return "builtin" }
func (stubBot) AllowedExecutors() []string            { return []string{"builtin"} }
func (stubBot) SwitchExecutor(string) (string, error) { return "", nil }
func (stubBot) Approve(string, string) error          { return nil }

// newTestRegistry 注册两条与被测命令无关的测试命令，help 用来验证未知命令的帮助文本。
func newTestRegistry() (*Registry, *Input, *int) {
	var calls int
	var captured Input
	r := NewRegistry()
	r.register(Definition{
		Name: "help", Summary: "列出命令与用法", Usage: "/help",
		Run: func(context.Context, Input) Result { return Result{Text: "帮助", OK: true} },
	})
	r.register(Definition{
		Name: "env", Aliases: []string{"e"}, Summary: "查看/修改环境变量",
		Usage: "/env [list|set K=V|unset K]",
		Run: func(_ context.Context, in Input) Result {
			calls++
			captured = in
			return Result{Text: strings.Join(in.Args, ","), OK: true}
		},
	})
	return r, &captured, &calls
}

func TestNonCommandPassesThrough(t *testing.T) {
	r, _, _ := newTestRegistry()
	for _, line := range []string{"hello", " hello ", "", "  ", "not /help", "a/b"} {
		if res, ok := r.Execute(context.Background(), stubBot{}, line); ok {
			t.Fatalf("Execute(%q) 不应被当作命令：%+v", line, res)
		}
	}
}

func TestUnknownCommandListsHelp(t *testing.T) {
	r, _, _ := newTestRegistry()
	res, ok := r.Execute(context.Background(), stubBot{}, "/nope")
	if !ok {
		t.Fatal("以 / 开头的文本应被当作命令")
	}
	if res.OK {
		t.Fatal("未知命令 OK 应为 false")
	}
	for _, want := range []string{"/help", "/env", "列出命令与用法", "/env [list|set K=V|unset K]"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("帮助文本缺少 %q：%q", want, res.Text)
		}
	}
}

func TestBareSlashIsUnknownCommand(t *testing.T) {
	r, _, _ := newTestRegistry()
	res, ok := r.Execute(context.Background(), stubBot{}, "  /  ")
	if !ok || res.OK || !strings.Contains(res.Text, "/help") {
		t.Fatalf("单独的 / 应返回帮助：%+v %v", res, ok)
	}
}

func TestAliasResolution(t *testing.T) {
	r, captured, calls := newTestRegistry()
	res, ok := r.Execute(context.Background(), stubBot{}, "/e list")
	if !ok || !res.OK {
		t.Fatalf("别名命令执行失败：%+v %v", res, ok)
	}
	if *calls != 1 {
		t.Fatalf("handler 调用次数 = %d，期望 1", *calls)
	}
	if !reflect.DeepEqual(captured.Args, []string{"list"}) {
		t.Fatalf("Args = %#v", captured.Args)
	}
}

func TestArgumentSplitting(t *testing.T) {
	r, captured, _ := newTestRegistry()
	for _, tc := range []struct {
		line string
		args []string
	}{
		{"/env list", []string{"list"}},
		{"/env  list", []string{"list"}},
		{"  /env \t set   K=V  ", []string{"set", "K=V"}},
		{"/env set K V", []string{"set", "K", "V"}},
		{"/env", nil},
	} {
		_, ok := r.Execute(context.Background(), stubBot{}, tc.line)
		if !ok {
			t.Fatalf("Execute(%q) 未被识别为命令", tc.line)
		}
		if !reflect.DeepEqual(captured.Args, tc.args) {
			t.Errorf("Execute(%q) Args = %#v，期望 %#v", tc.line, captured.Args, tc.args)
		}
	}
}

func TestNameIsCaseInsensitiveArgsPreserveCase(t *testing.T) {
	r, captured, _ := newTestRegistry()
	if res, ok := r.Execute(context.Background(), stubBot{}, "/ENV SeT Foo=Bar"); !ok || !res.OK {
		t.Fatalf("大写命令名执行失败：%+v %v", res, ok)
	}
	if !reflect.DeepEqual(captured.Args, []string{"SeT", "Foo=Bar"}) {
		t.Fatalf("Args = %#v，参数不应被改写", captured.Args)
	}
	if res, ok := r.Execute(context.Background(), stubBot{}, "/HeLp"); !ok || !res.OK || res.Text != "帮助" {
		t.Fatalf("混合大小写命令名执行失败：%+v %v", res, ok)
	}
}

func TestExecutePassesBotAndResultThrough(t *testing.T) {
	r := NewRegistry()
	bot := stubBot{}
	var gotBot BotAPI
	r.register(Definition{
		Name: "probe",
		Run: func(_ context.Context, in Input) Result {
			gotBot = in.Bot
			return Result{Text: "done", OK: false}
		},
	})
	res, ok := r.Execute(context.Background(), bot, "/probe")
	if !ok {
		t.Fatal("/probe 应被识别为命令")
	}
	if res.Text != "done" || res.OK {
		t.Fatalf("Result 未透传：%+v", res)
	}
	if gotBot != bot {
		t.Fatalf("Input.Bot 未透传：%#v", gotBot)
	}
}
