package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// recordingBot 记录命令对 BotAPI 的调用，供 builtin 命令测试使用。
type recordingBot struct {
	stopped    int
	cleared    bool
	tasks      string
	context    string
	skills     []string
	env        map[string]string
	envSets    []string
	envUnsets  []string
	model      string
	executor   string
	allowed    []string
	approvals  []string
	listEnvErr error
}

func (r *recordingBot) StopAll() int { return r.stopped }
func (r *recordingBot) ClearContext() error {
	r.cleared = true
	return nil
}
func (r *recordingBot) ListTasks() string    { return r.tasks }
func (r *recordingBot) ContextInfo() string  { return r.context }
func (r *recordingBot) ListSkills() []string { return r.skills }
func (r *recordingBot) ListEnv() (map[string]string, error) {
	return r.env, r.listEnvErr
}
func (r *recordingBot) SetEnv(name, value string) error {
	r.envSets = append(r.envSets, name+"="+value)
	return nil
}
func (r *recordingBot) UnsetEnv(name string) error {
	r.envUnsets = append(r.envUnsets, name)
	return nil
}
func (r *recordingBot) SwitchModel(name string) (string, error) {
	if name == "" {
		return r.model, nil
	}
	if name == "missing" {
		return "", errors.New("已配置的渠道里没有模型 missing")
	}
	return "已切换到 " + name, nil
}
func (r *recordingBot) DefaultExecutor() string { return r.executor }
func (r *recordingBot) AllowedExecutors() []string {
	if r.allowed == nil {
		return []string{"builtin"}
	}
	return r.allowed
}
func (r *recordingBot) SwitchExecutor(kind string) (string, error) {
	if kind == "" {
		return r.executor, nil
	}
	ok := false
	for _, allowed := range r.AllowedExecutors() {
		ok = ok || allowed == kind
	}
	if !ok {
		return "", fmt.Errorf("%s 不在白名单里，当前可用：%s", kind, strings.Join(r.AllowedExecutors(), "、"))
	}
	r.executor = kind
	return "已切换到 " + kind, nil
}
func (r *recordingBot) Approve(id, decision string) error {
	r.approvals = append(r.approvals, id+" "+decision)
	return nil
}

func builtinRegistry() *Registry {
	r := NewRegistry()
	RegisterBuiltins(r)
	return r
}

func run(t *testing.T, bot BotAPI, line string) Result {
	t.Helper()
	res, ok := builtinRegistry().Execute(context.Background(), bot, line)
	if !ok {
		t.Fatalf("%q 应被识别为命令", line)
	}
	return res
}

func TestRegisterBuiltinsRegistersTenCommands(t *testing.T) {
	defs := builtinRegistry().Definitions()
	if len(defs) != 10 {
		t.Fatalf("命令数量 = %d，期望 10", len(defs))
	}
	want := map[string]bool{"help": false, "stop": false, "clear": false, "model": false, "agent": false,
		"env": false, "skills": false, "context": false, "tasks": false, "approve": false}
	for _, d := range defs {
		if _, ok := want[d.Name]; !ok {
			t.Errorf("未预期的命令 %q", d.Name)
			continue
		}
		want[d.Name] = true
		if d.Summary == "" || d.Usage == "" || d.Run == nil {
			t.Errorf("命令 %q 定义不完整: %+v", d.Name, d)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("缺少命令 %q", name)
		}
	}
}

func TestHelpListsEveryCommand(t *testing.T) {
	res := run(t, &recordingBot{}, "/help")
	if !res.OK {
		t.Fatal("/help 应成功")
	}
	for _, name := range []string{"help", "stop", "clear", "model", "agent", "env", "skills", "context", "tasks", "approve"} {
		if !strings.Contains(res.Text, "/"+name) {
			t.Errorf("/help 缺少 /%s：%q", name, res.Text)
		}
	}
}

func TestUnknownCommandReturnsHelpAndNotOK(t *testing.T) {
	res := run(t, &recordingBot{}, "/nope")
	if res.OK {
		t.Fatal("未知命令的 OK 应为 false")
	}
	if !strings.Contains(res.Text, "/help") {
		t.Fatalf("未知命令应提示 /help：%q", res.Text)
	}
}

func TestAliasesResolve(t *testing.T) {
	if _, ok := builtinRegistry().Lookup("e"); !ok {
		t.Error("env 的别名 e 未注册")
	}
	if _, ok := builtinRegistry().Lookup("h"); !ok {
		t.Error("help 的别名 h 未注册")
	}
}

func TestStopReturnsCount(t *testing.T) {
	fb := &recordingBot{stopped: 2}
	res := run(t, fb, "/stop")
	if !res.OK || !strings.Contains(res.Text, "2") {
		t.Fatalf("应报告停掉 2 个任务：%+v", res)
	}
	zero := run(t, &recordingBot{}, "/stop")
	if !zero.OK || zero.Text == "" {
		t.Fatalf("没有任务时也应返回可读结果：%+v", zero)
	}
}

func TestClearCallsClearContext(t *testing.T) {
	fb := &recordingBot{}
	res := run(t, fb, "/clear")
	if !res.OK || !fb.cleared {
		t.Fatalf("应调用 ClearContext：%+v cleared=%v", res, fb.cleared)
	}
}

func TestModelShowsAndSwitches(t *testing.T) {
	fb := &recordingBot{model: "chat-x"}
	if res := run(t, fb, "/model"); !res.OK || !strings.Contains(res.Text, "chat-x") {
		t.Fatalf("无参应返回当前模型：%+v", res)
	}
	if res := run(t, fb, "/model next-model"); !res.OK || !strings.Contains(res.Text, "已切换到 next-model") {
		t.Fatalf("切换应返回确认：%+v", res)
	}
	if res := run(t, fb, "/model missing"); res.OK || !strings.Contains(res.Text, "missing") {
		t.Fatalf("未知模型应失败：%+v", res)
	}
	if res := run(t, fb, "/model a b"); res.OK {
		t.Fatalf("多余参数应失败：%+v", res)
	}
}

func TestAgentShowsAndSwitchesWithinWhitelist(t *testing.T) {
	fb := &recordingBot{executor: "builtin", allowed: []string{"builtin", "codex"}}
	if res := run(t, fb, "/agent"); !res.OK || !strings.Contains(res.Text, "默认执行器：builtin") || !strings.Contains(res.Text, "白名单：builtin、codex") {
		t.Fatalf("无参应返回默认与白名单：%+v", res)
	}
	if res := run(t, fb, "/agent list"); !res.OK || !strings.Contains(res.Text, "白名单：builtin、codex") || strings.Contains(res.Text, "默认执行器") {
		t.Fatalf("list 只列白名单：%+v", res)
	}
	if res := run(t, fb, "/agent codex"); !res.OK || !strings.Contains(res.Text, "已切换到 codex") || fb.executor != "codex" {
		t.Fatalf("白名单内应切换：%+v", res)
	}
	if res := run(t, fb, "/agent omp"); res.OK || !strings.Contains(res.Text, "白名单") {
		t.Fatalf("白名单外应拒绝并列出白名单：%+v", res)
	}
	if res := run(t, fb, "/agent builtin"); !res.OK || fb.executor != "builtin" {
		t.Fatalf("builtin 应切回内置：%+v", res)
	}
}

func TestEnvSetAcceptsEqualsAndSpace(t *testing.T) {
	fb := &recordingBot{}
	if res := run(t, fb, "/env set A=1"); !res.OK {
		t.Fatalf("K=V 写法应成功：%+v", res)
	}
	if res := run(t, fb, "/env set B 2"); !res.OK {
		t.Fatalf("K V 写法应成功：%+v", res)
	}
	if res := run(t, fb, "/env set C some value"); !res.OK {
		t.Fatalf("值里有空格时应整体作为值：%+v", res)
	}
	want := []string{"A=1", "B=2", "C=some value"}
	if len(fb.envSets) != len(want) {
		t.Fatalf("SetEnv 调用 = %v，期望 %v", fb.envSets, want)
	}
	for i := range want {
		if fb.envSets[i] != want[i] {
			t.Errorf("SetEnv[%d] = %q，期望 %q", i, fb.envSets[i], want[i])
		}
	}
}

func TestEnvUnset(t *testing.T) {
	fb := &recordingBot{env: map[string]string{"A": "1", "B": "2"}}
	if res := run(t, fb, "/env unset A"); !res.OK {
		t.Fatalf("unset 应成功：%+v", res)
	}
	if res := run(t, fb, "/env unset B"); !res.OK {
		t.Fatalf("unset 应成功：%+v", res)
	}
	if len(fb.envUnsets) != 2 || fb.envUnsets[0] != "A" || fb.envUnsets[1] != "B" {
		t.Fatalf("UnsetEnv 调用 = %v", fb.envUnsets)
	}
}

func TestEnvUnsetMissingKeyFails(t *testing.T) {
	if res := run(t, &recordingBot{}, "/env unset A"); res.OK || !strings.Contains(res.Text, "没有 A") {
		t.Fatalf("不存在的键应失败：%+v", res)
	}
}

func TestEnvListHidesValues(t *testing.T) {
	fb := &recordingBot{env: map[string]string{"ALPHA": "1"}}
	res := run(t, fb, "/env list")
	if !res.OK {
		t.Fatalf("/env list 应成功：%+v", res)
	}
	if !strings.Contains(res.Text, "ALPHA") || strings.Contains(res.Text, "=1") || !strings.Contains(res.Text, "<已隐藏>") {
		t.Fatalf("命令结果不能带值，应显示占位符：%q", res.Text)
	}
}

func TestEnvUsageErrors(t *testing.T) {
	for _, line := range []string{"/env set", "/env unset", "/env unset A=1", "/env nope", "/env set A=1 extra"} {
		res := run(t, &recordingBot{}, line)
		if res.OK || !strings.Contains(res.Text, "/env") {
			t.Errorf("%s 应返回用法提示且 OK=false：%+v", line, res)
		}
	}
}

func TestEnvListSorted(t *testing.T) {
	fb := &recordingBot{env: map[string]string{"ZED": "9", "ALPHA": "1"}}
	res := run(t, fb, "/env list")
	if !res.OK {
		t.Fatalf("/env list 应成功：%+v", res)
	}
	for _, want := range []string{"实例级", "ALPHA", "ZED", "<已隐藏>", "值请在设置的「环境变量」里查看"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("/env list 缺少 %q：%q", want, res.Text)
		}
	}
	for _, secret := range []string{"=1", "=9"} {
		if strings.Contains(res.Text, secret) {
			t.Errorf("/env list 不应输出变量的值（发现 %q）：%q", secret, res.Text)
		}
	}
	if strings.Index(res.Text, "ALPHA") > strings.Index(res.Text, "ZED") {
		t.Errorf("变量未按名字排序：%q", res.Text)
	}
}

func TestEnvListEmpty(t *testing.T) {
	res := run(t, &recordingBot{}, "/env")
	if !res.OK || !strings.Contains(res.Text, "/env set") {
		t.Fatalf("空环境应提示如何添加：%+v", res)
	}
}

func TestSkillsListsNames(t *testing.T) {
	fb := &recordingBot{skills: []string{"zeta", "alpha"}}
	res := run(t, fb, "/skills")
	if !res.OK || !strings.Contains(res.Text, "alpha") || !strings.Contains(res.Text, "zeta") {
		t.Fatalf("/skills 应列出技能：%+v", res)
	}
	empty := run(t, &recordingBot{}, "/skills")
	if !empty.OK || empty.Text == "" {
		t.Fatalf("没有技能时也应返回非空文本：%+v", empty)
	}
}

func TestContextAndTasksPassThrough(t *testing.T) {
	fb := &recordingBot{tasks: "共 1 个任务", context: "上下文：3 条消息"}
	if res := run(t, fb, "/tasks"); !res.OK || res.Text != fb.tasks {
		t.Fatalf("/tasks 应返回 ListTasks：%+v", res)
	}
	if res := run(t, fb, "/context"); !res.OK || res.Text != fb.context {
		t.Fatalf("/context 应返回 ContextInfo：%+v", res)
	}
}

func TestApproveMapsDecision(t *testing.T) {
	fb := &recordingBot{}
	if res := run(t, fb, "/approve appr_1 allow"); !res.OK {
		t.Fatalf("allow 应成功：%+v", res)
	}
	if res := run(t, fb, "/approve appr_2 deny"); !res.OK {
		t.Fatalf("deny 应成功：%+v", res)
	}
	if len(fb.approvals) != 2 || fb.approvals[0] != "appr_1 allow" || fb.approvals[1] != "appr_2 deny" {
		t.Fatalf("Approve 调用 = %v", fb.approvals)
	}
	for _, line := range []string{"/approve", "/approve id", "/approve id maybe"} {
		if res := run(t, fb, line); res.OK || !strings.Contains(res.Text, "/approve") {
			t.Errorf("%s 应返回用法提示且 OK=false：%+v", line, res)
		}
	}
}
