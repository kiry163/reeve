# reeve M2 实现计划：环境变量三层 / 斜线命令 / 技能

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 reeve 具备 spec §12/§14/§15 承诺的三块能力：三层环境变量（模型可读、写需审批）、服务端斜线命令（不进模型上下文）、本地 SKILL.md 技能（索引进提示、正文按需读）。

**Architecture:** 三个新包 `internal/envstore`、`internal/commands`、`internal/skills`，各自只依赖标准库；执行器侧通过 `ToolEnv` 新增字段拿到合并后的环境与技能集合，bot 层负责装配与审计事件。命令在消息进入聊天层之前解析，结果只走 `command.result` 事件。

**Tech Stack:** Go 1.22（零外部依赖）、React + Vite（沿用现有前端）。

**Spec:** `docs/superpowers/specs/2026-11-14-reeve-design.md`（§7 事件、§9 聊天层、§12 环境变量、§14 斜线命令、§15 技能、§16 接口）

## Global Constraints

- 零外部 Go 依赖；`go.mod` 保持只有 module + go。
- 审批默认 `untrusted`；策略只有 untrusted / on-request / never。
- 环境变量与密钥严格分家：env 明文可读；密钥只能 `{{secret:NAME}}`，永不进 env 存储、事件、模型上下文。
- `env.set` / `env.unset` 事件只记 `name`、`layer`、`source`，**不记值**。
- 命令在 HTTP 入口解析，不写 message、不叫醒聊天层、不进模型上下文；结果只写 `command.result` 事件。
- 技能只做本地 `SKILL.md`：无在线安装、无检索排序；解析失败逐条跳过并报错，不影响启动。
- 用户可见文案中文；错误信息说明原因与下一步。
- 每个任务收尾必须：`go build ./... && go vet ./... && go test -count=1 ./...` 全绿（涉及前端时加 `npm run build`），并单独提交。

---

## 文件结构

```
internal/envstore/envstore.go      三层环境变量：加载、校验、合并、写回
internal/envstore/envstore_test.go
internal/commands/registry.go      命令定义与查找（名/别名/摘要/用法）
internal/commands/builtin.go       10 个命令的 handler
internal/commands/execute.go       解析一行文本 → 结果
internal/commands/*_test.go
internal/skills/skills.go          SKILL.md 扫描、frontmatter 校验、索引
internal/skills/skills_test.go
internal/bot/env.go                bot 层装配：三层合并、审计事件、读写接口
internal/bot/commands.go           命令接入 Send、command.result 事件
internal/bot/skills.go             技能集合装配与索引
internal/builtin/agent.go          （改）ToolEnv 注入 EnvList 与 Skills
internal/builtin/tools.go          （改）shell 使用 EnvList；新增 list_env/read_skill
internal/builtin/envtools.go       新增：list_env / set_env / unset_env / read_skill
internal/bot/chat.go               （改）聊天层 list_env / read_skill 工具 + 技能索引
internal/bot/server.go             （改）env / commands / skills 端点
web/src/api.ts                     （改）reducer 处理 command.result；env/commands/skills API
web/src/components/SettingsDialog.tsx （改）环境变量与技能两个面板
App.tsx                            （改）渲染命令结果行
docs/smoke-m2.md                   冒烟记录
```

---

### Task 1: `internal/envstore` 包

**Files:**
- Create: `internal/envstore/envstore.go`、`internal/envstore/envstore_test.go`

**Interfaces:**
- Produces:
  - `type Layer string`（`LayerInstance = "instance"`, `LayerBot = "bot"`）
  - `func ValidName(name string) error`（`^[A-Za-z_][A-Za-z0-9_]*$`，长度 ≤ 128）
  - `func ValidValue(v string) error`（不含 `\n`、`\r`、NUL；长度 ≤ 8192）
  - `func LoadInstance(dir string) (map[string]string, error)`（读 `dir/env`，`KEY=VALUE` 每行，`#` 注释，缺失文件返回空 map）
  - `func SaveInstance(dir string, vars map[string]string) error`（排序写回，0600）
  - `func LoadBot(botDir string) (map[string]string, error)`（读 `botDir/env.json`）
  - `func SaveBot(botDir string, vars map[string]string) error`
  - `func Merge(base []string, layers ...map[string]string) []string`（后者覆盖前者；空字符串值表示删除该键；`PATH` 特例：按层序前置拼接，用 `:`/`;` 分隔）

- [ ] **Step 1: 写失败测试**

```go
package envstore

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestValidNameAndValue(t *testing.T) {
	for _, ok := range []string{"PATH", "A", "_X1", "NODE_OPTIONS"} {
		if err := ValidName(ok); err != nil {
			t.Fatalf("%s 应合法：%v", ok, err)
		}
	}
	for _, bad := range []string{"", "1A", "A-B", "A B", "A=B"} {
		if err := ValidName(bad); err == nil {
			t.Fatalf("%q 应非法", bad)
		}
	}
	if err := ValidValue("a\nb"); err == nil {
		t.Fatal("值含换行应非法")
	}
}

func TestMergeOverrideAndUnset(t *testing.T) {
	base := []string{"PATH=/usr/bin", "A=base", "B=keep"}
	got := Merge(base, map[string]string{"A": "bot", "C": "new"}, map[string]string{"C": "call", "B": ""})
	want := []string{"PATH=/usr/bin", "A=bot", "C=call"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestMergePathPrepends(t *testing.T) {
	got := Merge([]string{"PATH=/usr/bin"}, map[string]string{"PATH": "/opt/bin"}, map[string]string{"PATH": "/tmp/x"})
	if len(got) != 1 || got[0] != "PATH=/tmp/x:/opt/bin:/usr/bin" {
		t.Fatalf("PATH 应前置拼接，得到 %v", got)
	}
}

func TestInstanceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	vars := map[string]string{"PATH": "/usr/bin", "HTTPS_PROXY": "http://127.0.0.1:7890"}
	if err := SaveInstance(dir, vars); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(filepath.Join(dir, "env"))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("实例级 env 权限应 0600，得到 %v", st.Mode().Perm())
	}
	got, err := LoadInstance(dir)
	if err != nil || !reflect.DeepEqual(got, vars) {
		t.Fatalf("往返失败：%v %v", got, err)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/envstore -v`
Expected: FAIL（包不存在）

- [ ] **Step 3: 实现**

`Merge` 的行为：先把 base 解析成有序键列表；对每个 layer 按调用顺序覆盖值；值为 `""` 时从结果中删除该键；`PATH` 在每个 layer 出现时把它插到当前 PATH 前面（`newvalue + sep + oldvalue`）。写回时按 key 排序，格式 `KEY=VALUE\n`；`SaveInstance` 用 `os.WriteFile(path, data, 0o600)` 并在目录不存在时 `MkdirAll(dir, 0o700)`。

- [ ] **Step 4: 验证**

Run: `go test ./internal/envstore -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/envstore
git commit -m "feat(envstore): 三层环境变量的加载、校验与合并"
```

---

### Task 2: bot 层装配与审计事件

**Files:**
- Create: `internal/bot/env.go`、`internal/bot/env_test.go`
- Modify: `internal/bot/bot.go`（`Config.TokenDir` 旁新增 `EnvDir` 语义无需新字段：实例 env 就在 `REEVE_HOME`；bot 级在 `cfg.DataDir`）

**Interfaces:**
- Consumes: `envstore.*`（Task 1）
- Produces:
  - `func (b *Bot) InstanceEnv() (map[string]string, error)`
  - `func (b *Bot) BotEnv() (map[string]string, error)`
  - `func (b *Bot) SetBotEnv(name, value, source string) error`（校验 → 写盘 → 写 `env.set` 事件）
  - `func (b *Bot) UnsetBotEnv(name, source string) error`
  - `func (b *Bot) TaskEnv(call map[string]string) []string`（= `envstore.Merge(os.Environ(), instance, bot, call)`）
  - `func (b *Bot) EnvView() (instance, bot map[string]string, err error)`（供 UI 与 `list_env`）

- [ ] **Step 1: 写失败测试**

```go
func TestSetBotEnvWritesEventWithoutValue(t *testing.T) {
	b := newBot(t, t.TempDir(), nil)
	if err := b.SetBotEnv("HTTPS_PROXY", "http://127.0.0.1:7890", "chat"); err != nil {
		t.Fatal(err)
	}
	got, err := b.BotEnv()
	if err != nil || got["HTTPS_PROXY"] != "http://127.0.0.1:7890" {
		t.Fatalf("bot env 未写入：%v %v", got, err)
	}
	raw, err := os.ReadFile(filepath.Join(b.cfg.DataDir, "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "7890") {
		t.Fatal("env.set 事件不应包含值")
	}
	if !strings.Contains(string(raw), `"kind":"env.set"`) || !strings.Contains(string(raw), "HTTPS_PROXY") {
		t.Fatal("应写入含变量名的 env.set 事件")
	}
}

func TestTaskEnvLayers(t *testing.T) {
	b := newBot(t, t.TempDir(), nil)
	t.Setenv("REEVE_TEST_BASE", "base")
	_ = b.SetBotEnv("REEVE_TEST_BOT", "bot", "user")
	got := b.TaskEnv(map[string]string{"REEVE_TEST_CALL": "call", "REEVE_TEST_BOT": "call-wins"})
	joined := strings.Join(got, "\n")
	for _, want := range []string{"REEVE_TEST_BASE=base", "REEVE_TEST_BOT=call-wins", "REEVE_TEST_CALL=call"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少 %s\n%s", want, joined)
		}
	}
}
```

- [ ] **Step 2: 运行确认失败**：`go test ./internal/bot -run 'TestSetBotEnv|TestTaskEnv' -v` → FAIL

- [ ] **Step 3: 实现**

事件写入用 `b.store.Append("env.set", map[string]string{"layer": "bot", "name": name, "source": source}, false)`；`env.unset` 同理。实例级读写直接走 `envstore.LoadInstance/SaveInstance(b.cfg.TokenDir == "" ? b.cfg.DataDir : ...)` —— 注意默认 Bot 的 `DataDir` 就是 `REEVE_HOME`，非默认 Bot 的实例目录用 `cfg.TokenDir`（Task 1 of M1 已确保它指向主数据目录）。

- [ ] **Step 4: 验证**：`go test ./internal/bot -run 'TestSetBotEnv|TestTaskEnv' -v` → PASS

- [ ] **Step 5: 提交**：`git commit -m "feat: bot 层环境变量装配与审计事件"`

---

### Task 3: 执行器注入合并后的环境

**Files:**
- Modify: `internal/builtin/tools.go`（`ToolEnv` 新增 `EnvList []string`；shell 使用它）、`internal/builtin/agent.go`（`Task` 新增 `EnvList []string`；`runTool` 传入）
- Test: `internal/builtin/envlist_test.go`

**Interfaces:**
- Produces: `type ToolEnv struct { Workspace, TaskID string; Env map[string]string; EnvList []string; Skills *skills.Set }`；`Task.EnvList []string`
- 语义：`EnvList != nil` 时 shell 的 `cmd.Env` 直接取它；`Env`（密钥）在其后追加，重复键以后者为准。

- [ ] **Step 1: 写失败测试**

```go
func TestShellUsesEnvList(t *testing.T) {
	dir := t.TempDir()
	out, err := ShellTool.Execute(context.Background(), ToolEnv{Workspace: dir, EnvList: []string{"PATH=/usr/bin:/bin", "REEVE_X=from-envlist"}}, json.RawMessage(`{"command":"printf %s \"$REEVE_X\""}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "from-envlist") {
		t.Fatalf("shell 未使用 EnvList：%q", out)
	}
}
```

- [ ] **Step 2: 运行确认失败** → FAIL（`EnvList` 未定义）

- [ ] **Step 3: 实现**

shell 里把 `cmd.Env = SafeEnviron()` 改成：

```go
	if env.EnvList != nil {
		cmd.Env = env.EnvList
	} else {
		cmd.Env = SafeEnviron()
	}
	for k, v := range env.Env { // 密钥注入，覆盖同名项
		cmd.Env = append(cmd.Env, k+"="+v)
	}
```

`runTool` 构造 `ToolEnv{Workspace: t.Workspace, TaskID: t.ID, EnvList: t.EnvList, Skills: t.Skills}`。

- [ ] **Step 4: 验证**：`go test ./internal/builtin -run TestShellUsesEnvList -v` → PASS；`go test ./internal/...` 全绿

- [ ] **Step 5: 提交**：`git commit -m "feat(builtin): 执行器使用合并后的环境变量"`

---

### Task 4: 内置执行器工具 `list_env` / `set_env` / `unset_env`

**Files:**
- Create: `internal/builtin/envtools.go`、`internal/builtin/envtools_test.go`
- Modify: `internal/builtin/tools.go`（`DefaultTools` 追加三个工具）

**Interfaces:**
- Consumes: `ToolEnv.Env`（运行期值）、`ToolEnv.EnvStore`（新字段：一个窄接口，由 bot 注入）
- Produces:
  - `type EnvStore interface { View() (instance, bot map[string]string); Set(name, value, source string) error; Unset(name, source string) error }`
  - `list_env`（RiskRead）：输出三层来源与值；密钥只输出名字（`## 可用密钥` 段）
  - `set_env {"name","value"}`（RiskWrite）、`unset_env {"name"}`（RiskWrite）

- [ ] **Step 1: 写失败测试**

```go
func TestSetEnvNeedsApprovalAndPersists(t *testing.T) {
	// 用 recordingApprover：不批准时不应写入；批准后写入并产生 env.set 事件由 bot 层测试覆盖
	// 这里只验证工具的风险等级与 JSON 校验
	if got := setEnvTool.Risk(json.RawMessage(`{"name":"A","value":"1"}`)); got != RiskWrite {
		t.Fatalf("set_env 应为 write，得到 %v", got)
	}
	if _, err := setEnvTool.Execute(context.Background(), ToolEnv{}, json.RawMessage(`{"name":"1BAD","value":"x"}`)); err == nil {
		t.Fatal("非法变量名应报错")
	}
}
```

- [ ] **Step 2: 运行确认失败** → FAIL

- [ ] **Step 3: 实现**

工具描述里明确「值会以环境变量形式传给后续 shell；密钥请用密钥库，不要写进 env」。`Execute` 调 `env.EnvStore.Set/Unset`，`EnvStore == nil` 时返回错误「当前执行环境不支持修改环境变量」。`list_env` 输出：实例级、bot 级、调用级（若 `Env` 有值）三段，`KEY=VALUE`；末尾单列密钥名。

- [ ] **Step 4: 验证**：`go test ./internal/builtin -run 'Env' -v` → PASS

- [ ] **Step 5: 提交**：`git commit -m "feat(builtin): list_env/set_env/unset_env 工具"`

---

### Task 5: 聊天层只读环境工具

**Files:**
- Modify: `internal/bot/chat.go`（`chatTools()` 追加 `list_env`）、`internal/bot/chat_tools_readonly_test.go`

**Interfaces:**
- Consumes: `Bot.EnvView()`（Task 2）
- Produces: 聊天层 `list_env`（RiskRead，只读，永不显示密钥值）

- [ ] **Step 1: 写失败测试**：在 `TestChatLayerHasNoWriteOrExecTools` 的 `need` 列表加入 `list_env`，并在同一文件新增：

```go
func TestChatListEnvHidesSecrets(t *testing.T) {
	b := newBot(t, t.TempDir(), nil)
	defer b.Close()
	if err := b.vault.Set("API_TOKEN", "s3cret-value"); err != nil {
		t.Fatal(err)
	}
	var tool builtin.Tool
	for _, tl := range b.chatTools() {
		if tl.Name() == "list_env" {
			tool = tl
		}
	}
	out, err := tool.Execute(context.Background(), builtin.ToolEnv{}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "s3cret-value") {
		t.Fatal("list_env 泄露了密钥值")
	}
}
```

- [ ] **Step 2: 运行确认失败** → FAIL

- [ ] **Step 3: 实现**：`list_env` 输出实例级与 bot 级变量；不读密钥值，只附「密钥只能按名字用 {{secret:NAME}} 引用」的提示与名字列表。

- [ ] **Step 4: 验证**：`go test ./internal/bot -run 'ListEnv|TestChatLayerHasNoWrite' -v` → PASS

- [ ] **Step 5: 提交**：`git commit -m "feat(chat): 聊天层只读 list_env"`

---

### Task 6: env 的 HTTP 端点

**Files:**
- Modify: `internal/bot/server.go`（新增 4 条路由）、`internal/bot/server_env_test.go`

**Interfaces:**
- Produces:
  - `GET /api/env` → `{"vars": {K:V}}`（实例级）
  - `PUT /api/env` → body `{"vars": {K:V}}`，整体替换，校验失败 400 并指出变量名
  - `GET /api/bots/{id}/env` → `{"vars": {...}}`
  - `PUT /api/bots/{id}/env` → 整体替换，逐个写 `env.set`/`env.unset` 事件（`source=user`）

- [ ] **Step 1: 写失败测试**（httptest + `bs.Handler(nil)`）：PUT 一个含非法名的 body → 400 且错误信息包含该名；PUT 合法 body → GET 得到相同内容；bot 级 PUT 后 `stream.jsonl` 有 `env.set` 且无值。

- [ ] **Step 2: 运行确认失败** → FAIL（404）

- [ ] **Step 3: 实现**：沿用 `writeJSON`/`decodeJSON`；整体替换语义（body 中不存在的键会被删除），在响应里返回 `{"ok":true}`。

- [ ] **Step 4: 验证**：`go test ./internal/bot -run Env -v` → PASS

- [ ] **Step 5: 提交**：`git commit -m "feat(http): env 读写端点"`

---

### Task 7: `internal/commands` 注册表与执行器

**Files:**
- Create: `internal/commands/registry.go`、`internal/commands/execute.go`、`internal/commands/registry_test.go`、`internal/commands/execute_test.go`
- Modify: `internal/bot/commands.go`（新增，接入 Send）

**Interfaces:**
- Produces:
  - `type Definition struct { Name string; Aliases []string; Summary, Usage string; Run func(ctx context.Context, in Input) Result }`
  - `type Input struct { Args []string; Raw string; Bot BotAPI }`
  - `type BotAPI interface { StopAll() int; ClearContext() error; ListTasks() string; ContextInfo() string; ListSkills() []string; ListEnv() (instance, bot map[string]string, err error); SetEnv(name, value, global bool) error; UnsetEnv(name string, global bool) error; SwitchModel(name string) (string, error); SwitchExecutor(kind string) (string, error); Approve(id, decision string) error }`
  - `type Result struct { Text string; OK bool }`
  - `func NewRegistry() *Registry`、`func (r *Registry) Lookup(name string) (Definition, bool)`、`func (r *Registry) Definitions() []Definition`
  - `func (r *Registry) Execute(ctx context.Context, bot BotAPI, line string) (Result, bool)`（首字符不是 `/` 返回 `ok=false`）
  - `func (b *Bot) CommandLine(ctx context.Context, text string) (Result, bool)`：执行命令并写 `command.result` 事件

- [ ] **Step 1: 写失败测试**

```go
func TestNonCommandPassesThrough(t *testing.T) {
	if _, ok := NewRegistry().Execute(context.Background(), stubBot{}, "hello"); ok {
		t.Fatal("普通文本不应被当作命令")
	}
}

func TestUnknownCommandListsHelp(t *testing.T) {
	res, ok := NewRegistry().Execute(context.Background(), stubBot{}, "/nope")
	if !ok || res.OK || !strings.Contains(res.Text, "/help") {
		t.Fatalf("未知命令应返回帮助：%+v %v", res, ok)
	}
}

func TestHelpListsAllCommands(t *testing.T) {
	res, ok := NewRegistry().Execute(context.Background(), stubBot{}, "/help")
	if !ok || !strings.Contains(res.Text, "/env") || !strings.Contains(res.Text, "/approve") {
		t.Fatalf("帮助缺少命令：%q", res.Text)
	}
}
```

- [ ] **Step 2: 运行确认失败** → FAIL

- [ ] **Step 3: 实现注册表与解析**

解析规则：`strings.TrimSpace` 后必须以 `/` 开头；命令名取到第一个空白；参数按 `strings.Fields` 切；`/env set K=V` 支持 `K=V` 或 `K V`；`--global` 标记全局层。`Execute` 对未知名返回帮助文本且 `OK=false`。

- [ ] **Step 4: 验证**：`go test ./internal/commands -v` → PASS

- [ ] **Step 5: 提交**：`git commit -m "feat(commands): 命令注册表与解析器"`

---

### Task 8: 10 个命令的实现与 Send 接入

**Files:**
- Create: `internal/commands/builtin.go`、`internal/commands/builtin_test.go`
- Modify: `internal/bot/bot.go`（`Send` 开头解析命令）、`internal/bot/commands.go`

**Interfaces:**
- Consumes: Task 7 的 `Registry` 与 `BotAPI`；`Bot` 实现 `BotAPI`
- Produces:
  - `BotAPI` 的 `Bot` 实现（`StopAll` 复用 `Stop()`；`ClearContext` 把现有消息标 hidden 并 `turnGeneration++`；`ContextInfo` 用 `History` 条数与估算 token；`SwitchModel` 写 bot settings 的 `chat_model`；`SwitchExecutor` 只接受 `builtin`）
  - `Send` 语义：文本以 `/` 开头 → 不建 message、不 `wake()`，直接 `CommandLine`，把结果写 `command.result`（`{command, text, ok}`），返回一个 `Message{Role:"notice"}` 供 HTTP 响应；命令不得进入模型上下文。

- [ ] **Step 1: 写失败测试**

```go
func TestSendCommandDoesNotWakeChat(t *testing.T) {
	ai := &fakeAI{chat: func(_ context.Context, _ []builtin.Message) (builtin.Message, error) {
		t.Error("命令不应触发聊天层模型调用")
		return builtin.Message{}, nil
	}}
	b := newBot(t, t.TempDir(), ai)
	defer b.Close()
	if _, err := b.Send("/help"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if len(b.store.Unhandled()) != 0 {
		t.Fatal("命令不应留下未处理消息")
	}
	raw, _ := os.ReadFile(filepath.Join(b.cfg.DataDir, "stream.jsonl"))
	if !strings.Contains(string(raw), `"kind":"command.result"`) {
		t.Fatal("应写入 command.result 事件")
	}
}

func TestApproveCommandResolvesApproval(t *testing.T) {
	// 构造一个 pending 审批（记录型 approver + Register），/approve <id> allow 后记录状态为 allow
}
```

- [ ] **Step 2: 运行确认失败** → FAIL

- [ ] **Step 3: 实现 10 个命令**（语义见 spec §14；两处按 M2 决定）：
  - `/help`、`/stop`、`/clear`、`/model [name]`、`/agent [kind]`、`/env [list|set|unset] [--global]`、`/skills`、`/context`、`/tasks`、`/approve <id> allow|deny`
  - **用户命令直接生效**（用户就是授权者），写 `source=user` 的审计事件；模型发起的 `set_env` 才走审批。`/model`、`/agent` 同理。
  - `/agent` 目前只接受 `builtin`，其他值返回「M3 才支持外部执行器」。

- [ ] **Step 4: 验证**：`go test ./internal/bot -run 'Command|Approve' -v` → PASS

- [ ] **Step 5: 提交**：`git commit -m "feat(commands): 10 个斜线命令，接入 Send 且不唤醒聊天层"`

---

### Task 9: 命令的 HTTP 端点

**Files:**
- Modify: `internal/bot/server.go`、`internal/bot/server_commands_test.go`

**Interfaces:**
- Produces:
  - `GET /api/commands` → `{"commands":[{"name","aliases","summary","usage"}]}`
  - `POST /api/bots/{id}/commands` → body `{"line":"/env list"}`；返回 `{"text":..., "ok":...}`；非命令返回 400

- [ ] **Step 1: 写失败测试**：GET 返回 ≥10 条且含 `/env`；POST `/help` 返回 200 且 text 含 `/help`；POST 普通文本返回 400。

- [ ] **Step 2: 运行确认失败** → FAIL（404）

- [ ] **Step 3: 实现**：复用 `Bot.CommandLine`；写 `command.result` 事件的职责留在 `CommandLine`，避免双写。

- [ ] **Step 4: 验证**：`go test ./internal/bot -run Commands -v` → PASS

- [ ] **Step 5: 提交**：`git commit -m "feat(http): 命令列表与执行端点"`

---

### Task 10: `internal/skills` 包

**Files:**
- Create: `internal/skills/skills.go`、`internal/skills/skills_test.go`

**Interfaces:**
- Produces:
  - `type Skill struct { Name, Description, WhenToUse, Body, Path string }`
  - `type Set struct { skills []Skill; byName map[string]Skill; errs []error }`
  - `func Load(globalDir, botDir string) *Set`（bot 目录同名覆盖全局）
  - `func (s *Set) List() []Skill`、`func (s *Set) Get(name string) (Skill, bool)`、`func (s *Set) Errors() []error`
  - `func (s *Set) Index() string`（每个技能一行：`name — description — when_to_use`；无技能返回空串）
  - 格式：`SKILL.md` 以 `---` 包裹的 YAML frontmatter，必填 `name`/`description`/`when_to_use`；`name` 匹配 `^[a-z0-9][a-z0-9-]{1,63}$`；正文非空。

- [ ] **Step 1: 写失败测试**：合法技能被加载且 `Index()` 含三要素；缺 `when_to_use` → `Get` 取不到且 `Errors()` 非空；重名时 bot 目录覆盖全局；正文为空被跳过。

- [ ] **Step 2: 运行确认失败** → FAIL

- [ ] **Step 3: 实现**：frontmatter 用最小手工解析（不引依赖）：首行 `---`，到下一个 `---` 为 frontmatter，逐行 `key: value`，忽略未知键；正文为剩余内容 `TrimSpace`。

- [ ] **Step 4: 验证**：`go test ./internal/skills -v` → PASS

- [ ] **Step 5: 提交**：`git commit -m "feat(skills): SKILL.md 加载与索引"`

---

### Task 11: 技能注入与 `read_skill`

**Files:**
- Modify: `internal/bot/skills.go`（新增）、`internal/bot/bot.go`（启动时加载、任务创建时传 `Skills`）、`internal/bot/chat.go`（提示词追加索引 + 工具）、`internal/builtin/agent.go`（system prompt 追加索引）、`internal/builtin/envtools.go`（`read_skill` 工具）
- Test: `internal/bot/skills_inject_test.go`、`internal/builtin/skills_tool_test.go`

**Interfaces:**
- Consumes: `skills.Set`（Task 10）
- Produces:
  - `Bot.skills *skills.Set`，启动与 `POST /api/skills/reload`（可选）时重建
  - `Task.Skills *skills.Set`（`builtin.Task` 新字段，随 `ToolEnv.Skills` 传入）
  - 聊天层提示词末尾追加 `\n\n## 技能索引\n<index>`（为空则不追加）
  - `read_skill {"name"}`（RiskRead，两层都可用）：返回该技能正文与路径；不存在则报错并列出可用名

- [ ] **Step 1: 写失败测试**：建 `botDir/skills/pdf/SKILL.md` → 聊天层 prompt 含 `pdf —` 且 `read_skill` 返回正文；`read_skill` 未知名 → 错误含可用列表；执行器 system prompt 含索引。

- [ ] **Step 2: 运行确认失败** → FAIL

- [ ] **Step 3: 实现**：注意聊天层提示词由 `fmt.Sprintf(chatPrompt, name)` 生成，索引在 Sprintf 之后拼接；执行器侧在 `initialMessages` 里 `a.skillsIndex` 或 `t.Skills.Index()` 追加，放在 `## 记忆` 之后。

- [ ] **Step 4: 验证**：`go test ./internal/bot ./internal/builtin -run 'Skill' -v` → PASS

- [ ] **Step 5: 提交**：`git commit -m "feat(skills): 索引进提示词，正文按需 read_skill"`

---

### Task 12: 技能索引 HTTP 端点

**Files:**
- Modify: `internal/bot/server.go`、`internal/bot/server_skills_test.go`

**Interfaces:**
- Produces: `GET /api/skills` → `{"skills":[{"name","description","when_to_use","path"}],"errors":["..."]}`

- [ ] **Step 1: 写失败测试**：放置两个技能（一个合法、一个缺字段）→ 返回 1 条技能 + 1 条错误。

- [ ] **Step 2: 运行确认失败** → FAIL

- [ ] **Step 3: 实现**：只读；路径相对工作区/数据目录展示，不返回绝对 home。

- [ ] **Step 4: 验证**：`go test ./internal/bot -run Skills -v` → PASS

- [ ] **Step 5: 提交**：`git commit -m "feat(http): 技能索引端点"`

---

### Task 13: 网页适配

**Files:**
- Modify: `web/src/api.ts`（env/commands/skills API + reducer 处理 `command.result`）、`web/src/components/SettingsDialog.tsx`（新增「环境变量」「技能」两个 tab）、`web/src/App.tsx`（渲染命令结果行）、`web/src/index.css`（命令结果样式，复用现有 muted 样式）
- Test: `web` 构建（无 TS 错误）+ 手工验证清单

**Interfaces:**
- Consumes: Task 6/9/12 的端点
- Produces:
  - `api.ts` 新增：`env.get/save`、`botEnv.get/save`、`commands.list/run`、`skills.list`
  - reducer：`case 'command.result'` → 追加一个 `{type:'msg'}` item，`msgs[id] = {id: 'cmd_'+seq, role:'notice', text}`（`Msg` 类型加 `role: 'notice'`）
  - `App.tsx` 的 `Message`：`role === 'notice'` 时渲染为居中的等宽小字块，不带头像
  - `SettingsDialog`：环境变量面板可编辑实例级与当前 bot 级（表格：name/value/来源/删除），保存走 PUT；技能面板只读列出 name/description/when_to_use 与错误

- [ ] **Step 1: 改 `api.ts`**：加类型与调用；reducer 新分支；`Msg.role` 允许 `'notice'`。
- [ ] **Step 2: 改 `App.tsx`** 渲染 notice 行；`SettingsDialog` 加两个 tab（`navButton('env', ...)`、`navButton('skills', ...)`）与两个面板组件。
- [ ] **Step 3: 构建**：`cd web && npm run build` → 成功，`dist/index.html` 更新。
- [ ] **Step 4: 手工验证**（写入报告）：网页里改实例级 env → `/env list` 能看到；放一个技能 → 设置页能看到索引；在输入框发送 `/help` → 聊天里出现居中命令结果，且模型没有被调用（后台日志无模型请求）。
- [ ] **Step 5: 提交**：`git commit -m "feat(web): 环境变量与技能面板，命令结果行"`

---

### Task 14: 端到端冒烟与文档

**Files:**
- Create: `docs/smoke-m2.md`

- [ ] **Step 1: 全量验证**：`go build ./... && go vet ./... && go test -count=1 ./... && go test -race ./internal/...` + `cd web && npm run build`。
- [ ] **Step 2: 起真实服务（服务模式）**，用假模型验证：
  - 通过 `PUT /api/bots/main/env` 设 `REEVE_SMOKE=hello` → 派一个任务执行 `printf %s "$REEVE_SMOKE"` → 任务输出含 `hello`（证明执行器拿到合并环境）。
  - `POST /api/bots/main/commands {"line":"/env list"}` → 返回含 `REEVE_SMOKE`。
  - 放一个 `skills/demo/SKILL.md` → `GET /api/skills` 返回它；发消息让聊天层 `read_skill` 并复述正文首行（依赖假模型剧本，若剧本不支持则只验证 prompt 含索引，并如实记录）。
  - 发 `/help` → `stream.jsonl` 出现 `command.result`，且没有新的 `message`（用户消息）事件。
- [ ] **Step 3: 写 `docs/smoke-m2.md`**：命令、真实输出要点、未覆盖项（网页点击、真实模型、Android）。
- [ ] **Step 4: 提交**：`git commit -m "docs: M2 冒烟记录"`

---

## 自审

**spec 覆盖：**

| spec 章节 | 覆盖任务 |
|---|---|
| §7 事件 `env.set`/`env.unset`/`command.result` | Task 2、8 |
| §9 聊天层 `list_env` | Task 5 |
| §12 三层环境变量（存储/合并/优先级/PATH/校验/审计） | Task 1、2、3、4、6 |
| §13 密钥不进 env | Task 2（事件不含值）、Task 4（工具文案）、Task 5（`list_env` 不显示密钥值） |
| §14 十个斜线命令 | Task 7、8、9 |
| §15 技能（位置/格式/索引/按需读/错误跳过） | Task 10、11、12 |
| §16.2 `GET/PUT /api/env`、`/api/commands`、`/api/skills` | Task 6、9、12 |
| §16.3 Android | 不在 M2（M4） |

**与 spec 的两处有意偏离（需在同批更新 spec）：**

1. spec §14 表格里 `/env set|unset` 与 `/model`、`/agent` 标了「需审批」。M2 决定：**用户输入的命令直接生效**（用户就是授权者，再弹一次卡没有意义），模型发起的 `set_env` 才走审批；命令写入带 `source=user` 的审计事件。spec 对应列改为「用户命令直接生效；模型发起的改动需审批」。
2. spec §12 说「bot 级变更逐条写 `env.set`/`env.unset` 事件，含来源」——M2 明确事件**不含值**（值可能含代理凭据），UI 通过 `GET /api/bots/{id}/env` 读取。

**类型一致性：** `ToolEnv.EnvList []string`（Task 3）被 Task 4 的 `list_env` 与 shell 共用；`skills.Set` 在 Task 10 定义、Task 11 以 `ToolEnv.Skills *skills.Set` 与 `Task.Skills` 传递；`BotAPI` 在 Task 7 定义、Task 8 由 `Bot` 实现；`Result` 结构在 Task 7/8/9 一致使用。

**占位扫描：** 无 TBD/空函数；命令语义、校验规则、合并顺序、事件字段均已写死；技能 frontmatter 的必填键与命名规则明确。

---

## Rulings 回写（最终实现与计划原文的差异）

以下与上文计划原文不一致，实现以这里为准：

1. **`TaskEnv` 签名**：计划是 `func (b *Bot) TaskEnv(call map[string]string) []string`，最终是 `TaskEnv() []string`。`call` 在生产代码里只有 `TaskEnv(nil)` 一种调用，是死参数；调用级改由真正的生产者产生——`shell` 工具的 `env` 参数，执行前用 `envstore.Overlay(base, call)` 叠加（同名覆盖、空值删除、`PATH` 前置、与其它层共用 `ValidEntry` 校验）。
2. **`EnvStore` 签名**：计划是 `Set(name, value, source)` / `Unset(name, source)` / `View()`；最终拆成分层方法 `SetBotEnv` / `UnsetBotEnv` / `SetInstanceEnv` / `UnsetInstanceEnv` + `View()`，内置工具只能写 bot 级（`global` 只对用户命令与 HTTP 开放），审计来源仍是 `builtin`。
3. **`list_env` 输出**：计划写“实例级、bot 级、调用级三段”；最终只有实例级与 bot 级两段（`EnvReport`），另加一句密钥说明。调用级只存在于一次 `shell` 调用内部，工具看不到它，写出来只会是误导。
4. **无 reload 端点**：计划里可选的 `POST /api/skills/reload` 不做。技能只在启动时加载一次（支持符号链接目录），改技能后重启 reeve。
5. **技能端点是 bot scope**：计划写 `GET /api/skills`，最终是 `GET /api/bots/{id}/skills`（前缀转发后落到 Bot 自己的 `/api/skills`），返回该 Bot 合并后的技能索引（bot 同名技能覆盖实例级）；`path` 是 `skills/<目录>/SKILL.md` 相对标签，不暴露绝对路径。
6. **`/clear` 语义**：计划只说“清空当前会话上下文”。最终实现是：旧消息标 `hidden`、开启新 generation 作废进行中的回合，并且把未回答的 `ask_user` 卡片置为 `dismissed`、未填写的密钥卡片置为 `cancelled`（各自状态集内，网页不会卡在未知状态）。
