package bot

import (
	"context"
	"encoding/json"
	osexec "os/exec"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kiry163/reeve/internal/builtin"
)

// dispatchBot 建一个 Bot 并写入执行器白名单；默认聊天层策略是 untrusted。
// PATH/HOME 都指向临时目录：本机真实安装的 codex/pi/omp 绝不能被测到，更不能被启动。
func dispatchBot(t *testing.T, ex Executors, tweak func(*Config)) *Bot {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NVM_DIR", "")
	for _, name := range []string{"codex", "pi", "omp"} {
		if p, err := osexec.LookPath(name); err == nil {
			t.Fatalf("测试 PATH 里不应有真实执行器 %s（找到 %s）", name, p)
		}
	}
	b := newBotWith(t, t.TempDir(), nil, tweak)
	if err := b.SaveExecutors(ex); err != nil {
		t.Fatal(err)
	}
	return b
}

// runChatTool 直接执行聊天层工具，返回给模型看的文字。
func runChatTool(t *testing.T, b *Bot, name, args string) string {
	t.Helper()
	tool := b.registry().Get(name)
	if tool == nil {
		t.Fatalf("聊天层没有 %s", name)
	}
	out, err := tool.Execute(context.Background(), builtin.ToolEnv{Workspace: b.cfg.Workspace}, json.RawMessage(args))
	if err != nil {
		return "ERR: " + err.Error()
	}
	return out
}

func waitPending(t *testing.T, b *Bot, n int) []ApprovalRec {
	t.Helper()
	waitFor(t, "审批卡片", func() bool { return len(b.store.PendingApprovals()) == n })
	return b.store.PendingApprovals()
}

// 外部派活：先弹一张审批卡，审批前任务数为 0；批准后才写 task.created，执行器进记录。
func TestDispatchExternalRequiresApproval(t *testing.T) {
	b := dispatchBot(t, Executors{Allowed: []string{"builtin", "omp"}, Default: "builtin"}, nil)
	defer b.Close()

	const input = "把 internal/bot 的测试全部跑一遍并修掉失败，最后总结改了哪些文件"
	taskJSON, _ := json.Marshal(input)
	out := runChatTool(t, b, "start_task", `{"task":`+string(taskJSON)+`,"executor":"omp"}`)
	if !strings.Contains(out, "需要用户确认") {
		t.Fatalf("外部派活应要求确认，得到 %q", out)
	}
	aps := waitPending(t, b, 1)
	if n := len(b.store.Tasks()); n != 0 {
		t.Fatalf("审批前不应创建任务，得到 %d 个", n)
	}

	// 审批参数带执行器、任务 hash 和正文预览（供人判断），但不带完整 prompt 字段。
	ap := aps[0]
	var args struct {
		Executor string `json:"executor"`
		TaskHash string `json:"task_hash"`
		Preview  string `json:"preview"`
	}
	if err := json.Unmarshal(ap.Args, &args); err != nil {
		t.Fatal(err)
	}
	if args.Executor != "omp" || len(args.TaskHash) != 64 {
		t.Fatalf("审批参数应带执行器与任务 hash：%+v", args)
	}
	if args.Preview != input {
		t.Fatalf("审批卡片应显示正文预览：%q", args.Preview)
	}
	if strings.Contains(string(ap.Args), `"task"`) {
		t.Fatalf("审批参数不应带完整任务正文字段：%s", ap.Args)
	}

	if err := b.Decide(ap.ID, "once"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "批准后创建任务", func() bool { return len(b.store.Tasks()) == 1 })
	rec := b.store.Tasks()[0]
	if rec.Executor != "omp" {
		t.Fatalf("任务执行器应为 omp，得到 %q", rec.Executor)
	}
	if rec.Input != input {
		t.Fatalf("任务正文应原样保存：%q", rec.Input)
	}
	if a, ok := b.store.Approval(ap.ID); !ok || a.Status != "allow" {
		t.Fatalf("审批应记为已批准：%+v", a)
	}
}

// 白名单外直接返回错误：不弹卡、不建任务。
func TestDispatchOutsideWhitelistRejectsWithoutCard(t *testing.T) {
	b := dispatchBot(t, Executors{Allowed: []string{"builtin"}, Default: "builtin"}, nil)
	defer b.Close()

	out := runChatTool(t, b, "start_task", `{"task":"跑测试","executor":"omp"}`)
	if !strings.Contains(out, "白名单") {
		t.Fatalf("白名单外应直接报错，得到 %q", out)
	}
	if n := len(b.store.PendingApprovals()); n != 0 {
		t.Fatalf("白名单外不应弹卡片，得到 %d 条审批", n)
	}
	if n := len(b.store.Tasks()); n != 0 {
		t.Fatalf("白名单外不应创建任务，得到 %d 个", n)
	}
}

// 内置派活不需要审批，任务立刻创建，记录里执行器为空（= builtin）。
func TestDispatchBuiltinNeedsNoApproval(t *testing.T) {
	b := dispatchBot(t, defaultExecutors(), nil)
	defer b.Close()

	out := runChatTool(t, b, "start_task", `{"task":"跑测试"}`)
	if strings.Contains(out, "需要用户确认") {
		t.Fatalf("内置派活不该要审批：%q", out)
	}
	if n := len(b.store.PendingApprovals()); n != 0 {
		t.Fatalf("内置派活不应产生审批，得到 %d", n)
	}
	if n := len(b.store.Tasks()); n != 1 {
		t.Fatalf("内置派活应立刻建任务，得到 %d 个", n)
	}
	if rec := b.store.Tasks()[0]; rec.Executor != "" {
		t.Fatalf("内置任务的执行器应为空，得到 %q", rec.Executor)
	}
}

// 默认执行器是外部时，省略 executor 也要审批。
func TestDispatchDefaultExternalRequiresApproval(t *testing.T) {
	b := dispatchBot(t, Executors{Allowed: []string{"builtin", "omp"}, Default: "omp"}, nil)
	defer b.Close()

	out := runChatTool(t, b, "start_task", `{"task":"跑测试"}`)
	if !strings.Contains(out, "需要用户确认") {
		t.Fatalf("默认执行器是外部时应要求确认，得到 %q", out)
	}
	aps := waitPending(t, b, 1)
	var args struct {
		Executor string `json:"executor"`
	}
	if err := json.Unmarshal(aps[0].Args, &args); err != nil {
		t.Fatal(err)
	}
	if args.Executor != "omp" {
		t.Fatalf("审批应绑定默认执行器 omp，得到 %+v", args)
	}
	if n := len(b.store.Tasks()); n != 0 {
		t.Fatalf("审批前不应创建任务，得到 %d 个", n)
	}
}

// on-request 策略本来会放行 exec 风险；外部派活仍必须走审批卡片。
func TestDispatchExternalAsksUnderOnRequestPolicy(t *testing.T) {
	b := dispatchBot(t, Executors{Allowed: []string{"builtin", "omp"}, Default: "builtin"},
		func(c *Config) { c.ChatPolicy = builtin.PolicyOnRequest })
	defer b.Close()

	out := runChatTool(t, b, "start_task", `{"task":"跑测试","executor":"omp"}`)
	if !strings.Contains(out, "需要用户确认") {
		t.Fatalf("on-request 下外部派活仍应确认，得到 %q", out)
	}
	waitPending(t, b, 1)
	if n := len(b.store.Tasks()); n != 0 {
		t.Fatalf("审批前不应创建任务，得到 %d 个", n)
	}
}

// never 策略：外部派活直接被拒绝，不弹卡也不建任务。
func TestNeverPolicyDeniesExternalDispatch(t *testing.T) {
	b := dispatchBot(t, Executors{Allowed: []string{"builtin", "omp"}, Default: "builtin"},
		func(c *Config) { c.ChatPolicy = builtin.PolicyNever })
	defer b.Close()

	out := runChatTool(t, b, "start_task", `{"task":"跑测试","executor":"omp"}`)
	if !strings.Contains(out, "没有批准") && !strings.Contains(out, "拒绝") {
		t.Fatalf("never 策略下应直接拒绝：%q", out)
	}
	if n := len(b.store.PendingApprovals()); n != 0 {
		t.Fatalf("never 策略下不应弹卡，得到 %d 条审批", n)
	}
	if n := len(b.store.Tasks()); n != 0 {
		t.Fatalf("被拒绝的派活不该建任务，得到 %d", n)
	}
}

// session 批准按既有 scope 语义生效：它绑定到发起操作的聊天任务（每次派活都是新的临时操作），
// 只放行当次派活；同样参数再派一次仍要重新确认，不会留下长期通行证。
func TestDispatchSessionApprovalAppliesOnce(t *testing.T) {
	b := dispatchBot(t, Executors{Allowed: []string{"builtin", "omp"}, Default: "builtin"}, nil)
	defer b.Close()

	const args = `{"task":"跑测试","executor":"omp"}`
	if out := runChatTool(t, b, "start_task", args); !strings.Contains(out, "需要用户确认") {
		t.Fatalf("第一次外部派活应要求确认，得到 %q", out)
	}
	ap := waitPending(t, b, 1)[0]
	if err := b.Decide(ap.ID, "session"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session 批准后创建任务", func() bool { return len(b.store.Tasks()) == 1 })
	if a, ok := b.store.Approval(ap.ID); !ok || a.Status != "allow" || a.Scope != "session" {
		t.Fatalf("审批应记为 session 批准：%+v", a)
	}

	// 同一执行器 + 同一任务文本：新的派活是新的聊天操作，session 批准不能跨操作复用。
	if out := runChatTool(t, b, "start_task", args); !strings.Contains(out, "需要用户确认") {
		t.Fatalf("第二次派活仍应要求确认，得到 %q", out)
	}
	waitPending(t, b, 1)
	if n := len(b.store.Tasks()); n != 1 {
		t.Fatalf("未批准的第二次派活不应建任务，得到 %d 个", n)
	}
}

// queue_task 走同一条派活审批路径；hold=true 在批准后仍保持待办状态。
func TestQueueTaskExternalHoldsAfterApproval(t *testing.T) {
	b := dispatchBot(t, Executors{Allowed: []string{"builtin", "omp"}, Default: "builtin"}, nil)
	defer b.Close()

	out := runChatTool(t, b, "queue_task", `{"task":"整理照片","executor":"omp","hold":true}`)
	if !strings.Contains(out, "需要用户确认") {
		t.Fatalf("外部待办也应要求确认，得到 %q", out)
	}
	ap := waitPending(t, b, 1)[0]
	if err := b.Decide(ap.ID, "once"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "批准后创建待办", func() bool { return len(b.store.Tasks()) == 1 })
	rec := b.store.Tasks()[0]
	if rec.Executor != "omp" || rec.Status != "paused" || rec.StopReason != "held" {
		t.Fatalf("待办应保持暂停且执行器为 omp：%+v", rec)
	}
}

// 无聊天模型的直连路径不弹审批卡，但白名单是硬闸门。
func TestDirectCLIRespectsWhitelist(t *testing.T) {
	b := dispatchBot(t, Executors{Allowed: []string{"builtin"}, Default: "builtin"}, nil)
	defer b.Close()

	b.runDirectCLI(context.Background(), []Message{{ID: "msg_direct_1", Role: "user", Text: "跑一下", CreatedAt: time.Now()}}, "omp")
	if n := len(b.store.Tasks()); n != 0 {
		t.Fatalf("白名单外不应建任务，得到 %d 个", n)
	}
	if n := countMsgs(b.store, "assistant", "白名单"); n != 1 {
		t.Fatalf("白名单外应回复错误说明，得到 %d 条", n)
	}
	if n := len(b.store.PendingApprovals()); n != 0 {
		t.Fatalf("直连路径不应弹审批卡，得到 %d 条", n)
	}

	if err := b.SaveExecutors(Executors{Allowed: []string{"builtin", "omp"}, Default: "builtin"}); err != nil {
		t.Fatal(err)
	}
	b.runDirectCLI(context.Background(), []Message{{ID: "msg_direct_2", Role: "user", Text: "跑一下", CreatedAt: time.Now()}}, "omp")
	if n := len(b.store.Tasks()); n != 1 {
		t.Fatalf("白名单内应直接建任务，得到 %d 个", n)
	}
	if rec := b.store.Tasks()[0]; rec.Executor != "omp" || !rec.DirectChat {
		t.Fatalf("直连任务应标记执行器与 direct：%+v", rec)
	}
	if n := len(b.store.PendingApprovals()); n != 0 {
		t.Fatalf("直连路径不应弹审批卡，得到 %d 条", n)
	}
}

// 工具 schema 把白名单暴露为 executor 的 enum，并说明省略即默认执行器。
func TestDispatchSchemaExposesWhitelist(t *testing.T) {
	b := dispatchBot(t, Executors{Allowed: []string{"builtin", "omp", "pi"}, Default: "omp"}, nil)
	defer b.Close()

	for _, name := range []string{"start_task", "queue_task"} {
		var schema struct {
			Properties map[string]struct {
				Enum        []string `json:"enum"`
				Description string   `json:"description"`
			} `json:"properties"`
		}
		tools := b.registry().Specs()
		var schemaRaw json.RawMessage
		for _, tl := range tools {
			if tl.Name == name {
				schemaRaw = tl.Schema
			}
		}
		if schemaRaw == nil {
			t.Fatalf("聊天层没有 %s", name)
		}
		if err := json.Unmarshal(schemaRaw, &schema); err != nil {
			t.Fatalf("%s schema 不是合法 JSON：%v", name, err)
		}
		ex := schema.Properties["executor"]
		if len(ex.Enum) != 3 || !strings.Contains(strings.Join(ex.Enum, ","), "omp") {
			t.Fatalf("%s 的 executor enum 应来自白名单：%+v", name, ex)
		}
		if !strings.Contains(ex.Description, "省略") || !strings.Contains(ex.Description, "omp") {
			t.Fatalf("%s 应说明省略 = 默认执行器 omp：%q", name, ex.Description)
		}
	}
}

// 审批不设超时：批准时如果执行器已经被移出白名单，必须拒绝 commit，不能建任务。
func TestDispatchRechecksWhitelistBeforeCommit(t *testing.T) {
	b := dispatchBot(t, Executors{Allowed: []string{"builtin", "omp"}, Default: "builtin"}, nil)
	defer b.Close()

	if out := runChatTool(t, b, "start_task", `{"task":"跑测试","executor":"omp"}`); !strings.Contains(out, "需要用户确认") {
		t.Fatalf("外部派活应要求确认，得到 %q", out)
	}
	ap := waitPending(t, b, 1)[0]
	// 卡片还挂着的时候把 omp 移出白名单。
	if err := b.SaveExecutors(Executors{Allowed: []string{"builtin"}, Default: "builtin"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Decide(ap.ID, "once"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "说明派活没有执行", func() bool { return countMsgs(b.store, "notice", "白名单") == 1 })
	if n := len(b.store.Tasks()); n != 0 {
		t.Fatalf("白名单复查失败时不应建任务，得到 %d 个", n)
	}
}

// 排队中的外部任务在启动前还要再过一次白名单：失败走 failExternal，错误文案带白名单。
func TestExternalStartRechecksWhitelist(t *testing.T) {
	b := dispatchBot(t, Executors{Allowed: []string{"builtin"}, Default: "builtin"}, nil)
	defer b.Close()

	const id = "task_stale_external"
	b.store.Append("task.created", TaskRec{ID: id, Title: "t", Input: "x", Executor: "omp",
		Status: "queued", CreatedAt: time.Now()}, false)
	rec, _ := b.store.Task(id)
	b.startExternal(rec)
	waitFor(t, "任务失败", func() bool {
		r, ok := b.store.Task(id)
		return ok && r.Status == "failed"
	})
	rec, _ = b.store.Task(id)
	if !strings.Contains(rec.Error, "白名单") {
		t.Fatalf("失败文案应说明白名单：%q", rec.Error)
	}
}

// 模型显式传长 title 时，审批参数里的 title 只留 20 个字符；正文只留前 400 个字符的预览。
func TestDispatchApprovalLongTitleAndPreview(t *testing.T) {
	b := dispatchBot(t, Executors{Allowed: []string{"builtin", "omp"}, Default: "builtin"}, nil)
	defer b.Close()

	input := "正文内容BODY_MARKER " + strings.Repeat("很长的任务描述", 100)
	longTitle := strings.Repeat("超长标题", 20) + "TITLE_MARKER"
	taskJSON, _ := json.Marshal(input)
	titleJSON, _ := json.Marshal(longTitle)
	out := runChatTool(t, b, "start_task", `{"task":`+string(taskJSON)+`,"title":`+string(titleJSON)+`,"executor":"omp"}`)
	if !strings.Contains(out, "需要用户确认") {
		t.Fatalf("外部派活应要求确认，得到 %q", out)
	}
	ap := waitPending(t, b, 1)[0]
	raw := string(ap.Args)
	if strings.Contains(raw, "TITLE_MARKER") || strings.Contains(raw, input) {
		t.Fatalf("审批参数不应包含完整 title 或完整正文：%s", raw)
	}
	var args struct {
		Title   string `json:"title"`
		Preview string `json:"preview"`
	}
	if err := json.Unmarshal(ap.Args, &args); err != nil {
		t.Fatal(err)
	}
	if args.Title == "" || utf8.RuneCountInString(args.Title) > 20 {
		t.Fatalf("审批参数里的 title 应非空且 ≤20 个字符：%q（%d）", args.Title, utf8.RuneCountInString(args.Title))
	}
	if want := string([]rune(input)[:400]); args.Preview != want {
		t.Fatalf("预览应是正文前 400 个字符：len=%d", utf8.RuneCountInString(args.Preview))
	}
}

// 改白名单后立刻重建聊天层工具：模型不用重启就能看到新的 executor 枚举。
func TestSaveExecutorsRefreshesDispatchSchema(t *testing.T) {
	b := dispatchBot(t, Executors{Allowed: []string{"builtin"}, Default: "builtin"}, nil)
	defer b.Close()

	schema := string(b.registry().Get("start_task").Schema())
	if strings.Contains(schema, `"omp"`) {
		t.Fatalf("改白名单前不该有 omp：%s", schema)
	}
	if err := b.SaveExecutors(Executors{Allowed: []string{"builtin", "omp"}, Default: "builtin"}); err != nil {
		t.Fatal(err)
	}
	schema = string(b.registry().Get("start_task").Schema())
	if !strings.Contains(schema, `"omp"`) {
		t.Fatalf("改白名单后 schema 应立即包含 omp：%s", schema)
	}
}

// 保存白名单会重建聊天工具表：读侧走原子指针，-race 下不能有数据竞争，也不能读到 nil。
func TestExecutorReloadIsRaceFree(t *testing.T) {
	b := dispatchBot(t, Executors{Allowed: []string{"builtin"}, Default: "builtin"}, nil)
	defer b.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			allowed := []string{"builtin"}
			if i%2 == 1 {
				allowed = []string{"builtin", "omp"}
			}
			if err := b.SaveExecutors(Executors{Allowed: allowed, Default: "builtin"}); err != nil {
				t.Errorf("第 %d 次保存白名单失败：%v", i, err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 400; i++ {
			reg := b.registry()
			if reg == nil {
				t.Error("聊天工具表不应为 nil")
				return
			}
			if reg.Get("start_task") == nil {
				t.Error("重建期间应始终能取到 start_task")
				return
			}
		}
	}()
	wg.Wait()
}

// 预览最多 400 个字符（按 rune），完整长任务不会整体进审批参数。
func TestDispatchApprovalPreviewTruncatesLongInput(t *testing.T) {
	b := dispatchBot(t, Executors{Allowed: []string{"builtin", "omp"}, Default: "builtin"}, nil)
	defer b.Close()

	long := strings.Repeat("长", 500)
	taskJSON, _ := json.Marshal(long)
	out := runChatTool(t, b, "start_task", `{"task":`+string(taskJSON)+`,"executor":"omp"}`)
	if !strings.Contains(out, "需要用户确认") {
		t.Fatalf("外部派活应要求确认，得到 %q", out)
	}
	ap := waitPending(t, b, 1)[0]
	var args struct {
		Preview string `json:"preview"`
	}
	if err := json.Unmarshal(ap.Args, &args); err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(args.Preview)); n != 400 {
		t.Fatalf("预览应截到 400 个字符，得到 %d", n)
	}
	if strings.Contains(string(ap.Args), long) {
		t.Fatal("审批参数不应包含完整长任务")
	}
}

// 重试沿用原任务的执行器，并在白名单变化后拒绝重试（不再静默换成默认执行器）。
func TestRetryKeepsExecutorAndChecksWhitelist(t *testing.T) {
	b := dispatchBot(t, Executors{Allowed: []string{"builtin", "omp"}, Default: "builtin"}, nil)
	defer b.Close()

	b.store.Append("task.created", TaskRec{ID: "task_retry_one", Title: "重试", Input: "跑测试",
		Executor: "omp", Status: "completed"}, false)
	id, err := b.RetryTask("task_retry_one")
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := b.store.Task(id)
	if !ok {
		t.Fatalf("重试没有创建任务：%s", id)
	}
	if rec.Executor != "omp" {
		t.Fatalf("重试应沿用原执行器 omp，得到 %q", rec.Executor)
	}

	if err := b.SaveExecutors(Executors{Allowed: []string{"builtin"}, Default: "builtin"}); err != nil {
		t.Fatal(err)
	}
	b.store.Append("task.created", TaskRec{ID: "task_retry_two", Title: "重试", Input: "再跑一次",
		Executor: "omp", Status: "completed"}, false)
	before := len(b.store.Tasks())
	if _, err := b.RetryTask("task_retry_two"); err == nil || !strings.Contains(err.Error(), "白名单") {
		t.Fatalf("白名单外的执行器不能重试：%v", err)
	}
	if after := len(b.store.Tasks()); after != before {
		t.Fatalf("被拒绝的重试不应创建任务：%d -> %d", before, after)
	}
}

// 执行器被移出白名单后，恢复外部任务必须失败并保持 paused。
func TestResumeExternalOutsideWhitelistStaysPaused(t *testing.T) {
	b := dispatchBot(t, Executors{Allowed: []string{"builtin", "omp"}, Default: "builtin"}, nil)
	defer b.Close()

	b.store.Append("task.created", TaskRec{ID: "task_resume_guard", Title: "暂停", Input: "x",
		Executor: "omp", Status: "paused", StopReason: "user"}, false)
	if err := b.SaveExecutors(Executors{Allowed: []string{"builtin"}, Default: "builtin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ResumeTask("task_resume_guard", ""); err == nil || !strings.Contains(err.Error(), "白名单") {
		t.Fatalf("白名单外恢复应报错：%v", err)
	}
	rec, _ := b.store.Task("task_resume_guard")
	if rec.Status != "paused" {
		t.Fatalf("恢复失败后应保持 paused，得到 %q", rec.Status)
	}
}

// 外部任务的 steer_task 一律不支持：如实说明无法插话，也不假装记下补充。
func TestSteerExternalTaskUnsupported(t *testing.T) {
	b := dispatchBot(t, Executors{Allowed: []string{"builtin", "omp"}, Default: "builtin"}, nil)
	defer b.Close()

	b.store.Append("task.created", TaskRec{ID: "task_steer_ext", Title: "外部", Input: "x",
		Executor: "omp", Status: "running"}, false)
	out := runChatTool(t, b, "steer_task", `{"task_id":"task_steer_ext","text":"改成中文"}`)
	if !strings.Contains(out, "无法插话") || !strings.Contains(out, "不会转达") {
		t.Fatalf("应如实说明外部执行器无法插话：%q", out)
	}
	if rec, _ := b.store.Task("task_steer_ext"); len(rec.Notes) != 0 {
		t.Fatalf("补充要求不应被记下（不会转达）：%+v", rec.Notes)
	}
}
