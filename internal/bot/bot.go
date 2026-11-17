// Package bot 是 reeve：一个聊天层（快模型 + 管理工具）+ 后台 agent + 监工。
package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kiry163/reeve/internal/builtin"
	"github.com/kiry163/reeve/internal/commands"
	"github.com/kiry163/reeve/internal/exec"
	"github.com/kiry163/reeve/internal/exec/cli"
	"github.com/kiry163/reeve/internal/skills"
	"github.com/kiry163/reeve/internal/vault"
)

type Config struct {
	Name          string
	DataDir       string // 事件日志、记忆
	RootDir       string // 实例级数据目录（默认同 DataDir）；子 Bot 用它找实例级 env 等共享文件
	Workspace     string // 后台 agent 的工作区
	ChatProvider  builtin.Provider
	ChatModel     string
	AgentProvider builtin.Provider
	AgentModel    string
	Policy        builtin.Policy
	ChatPolicy    builtin.Policy // 聊天层派外部执行器任务、删除密钥时的审批模式，默认 untrusted（每次都要你确认）
	Rules         builtin.Rules
	MaxConcurrent int           // 所有 Bot 合计同时运行的后台任务上限（MAX_CONCURRENT），默认 3
	TaskMaxTime   time.Duration // 单个任务最长运行时间，默认 30 分钟
	ContextTokens int           // 后台任务对话超过多少 token（估算）就压缩旧内容，默认 60000（CONTEXT_TOKENS）
	StallTimeout  time.Duration // 内置任务多久没有事件算卡住，默认 90 秒
	// ExternalStallTimeout 是外部执行器多久没有输出算卡住，默认 10 分钟。
	// CLI 模型一次思考几分钟很常见，心跳只能证明进程活着，不能证明它在干活。
	ExternalStallTimeout time.Duration
	TurnTimeout          time.Duration // 聊天层每轮上限，默认 90 秒
	MasterKey            string        // REEVE_MASTER_KEY；为空时用 data/master.key（自动生成）
	Demo                 bool
	InitialBaseURL       string
	InitialKey           string
}

type Bot struct {
	cfg      Config
	store    *Store
	mgr      *builtin.Manager
	execs    *exec.Registry // codex/pi/omp 外部执行器
	approver *builtin.QueueApprover
	chatTool atomic.Pointer[builtin.Registry] // 聊天层工具表；白名单变化时整体替换，读侧统一走 registry()
	chatG    *builtin.Guard
	guard    *builtin.Guard // 任务和聊天层共用的审批管线
	vault    *vault.Vault
	redactor *redactor // 主密钥 + 密钥库 + 网关令牌的统一脱敏
	sch      sched
	skills   *skills.Set        // 启动时加载一次的技能集合（全局 + bot 私有）
	commands *commands.Registry // 斜线命令注册表，构造后只读

	modelMu        sync.RWMutex
	memoryMu       sync.Mutex
	envMu          sync.Mutex   // 串行化实例 env 的读-改-写（含整体替换）
	pool           *channelPool // 设备上所有 Bot 共用的 API 渠道
	hasRoutes      bool         // 本 Bot 保存过模型选择（否则退回启动配置里的模型）
	chatModel      string
	agentModelName string          // 后台 Agent 的模型，可与聊天层分开
	envModel       modelConnection // 环境变量提供的模型连接，非空时优先（聊天）
	envAgent       modelConnection // 环境变量提供的 Agent 连接（AGENT_MODEL，未设时同聊天模型）
	defaultChannel string          // 聊天用的渠道 ID
	agentChannel   string          // Agent 用的渠道 ID
	executors      Executors       // 本 Bot 的执行器白名单、默认执行器与 CLI 选项，由 modelMu 保护
	modelOwner     *Bot
	slots          *taskSlots // 所有 Bot 共用的后台任务并发上限
	coordinator    *Bots
	profile        atomic.Pointer[BotProfile]
	shuttingDown   atomic.Bool // 优雅停机中：运行中的外部任务要落 interrupted 而不是 cancelled

	ctx        context.Context
	cancel     context.CancelFunc
	kick       chan struct{}
	wg         sync.WaitGroup
	background sync.WaitGroup

	turnMu         sync.Mutex
	turnCancel     context.CancelCauseFunc
	turnGeneration uint64   // invalidates pending snapshots when Stop wins the race
	activeTurnIDs  []string // 当前回合正在处理的输入（/clear 时必须作废），受 turnMu 保护
}

var (
	errStopped      = errors.New("用户叫停")
	errReplyStopped = errors.New("用户停止了这条回复")
)

func New(cfg Config) (*Bot, error) {
	return newRuntime(cfg, nil, nil)
}

// applyDefaults 把 Config 里没填的字段补成产品默认值。
// 只负责赋值，不校验：审批策略的合法性仍由 newRuntime 在补完默认值后检查。
func applyDefaults(cfg *Config) {
	if cfg.Name == "" {
		cfg.Name = "reeve"
	}
	if cfg.Policy == "" {
		cfg.Policy = builtin.PolicyUntrusted
	}
	if cfg.ChatPolicy == "" {
		cfg.ChatPolicy = builtin.PolicyUntrusted
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 3
	}
	if cfg.TaskMaxTime <= 0 {
		cfg.TaskMaxTime = 30 * time.Minute
	}
	if cfg.StallTimeout <= 0 {
		cfg.StallTimeout = 90 * time.Second
	}
	if cfg.ExternalStallTimeout <= 0 {
		cfg.ExternalStallTimeout = 10 * time.Minute
	}
	if cfg.TurnTimeout <= 0 {
		cfg.TurnTimeout = 90 * time.Second
	}
	if cfg.ContextTokens <= 0 {
		cfg.ContextTokens = 60000
	}
}

func newRuntime(cfg Config, modelOwner *Bot, profile *BotProfile) (*Bot, error) {
	applyDefaults(&cfg)
	if !builtin.ValidPolicy(cfg.Policy) {
		return nil, fmt.Errorf("审批策略 %q 不支持，可选 untrusted / on-request / never", cfg.Policy)
	}
	if !builtin.ValidPolicy(cfg.ChatPolicy) {
		return nil, fmt.Errorf("聊天层审批策略 %q 不支持，可选 untrusted / on-request / never", cfg.ChatPolicy)
	}
	if cfg.ChatProvider == nil {
		cfg.ChatProvider = cfg.AgentProvider
	}
	if err := os.MkdirAll(cfg.Workspace, 0o755); err != nil {
		return nil, err
	}
	store, err := OpenStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	initialized := false
	defer func() {
		if !initialized {
			store.Close()
		}
	}()
	key, generated, err := vault.LoadKey(cfg.MasterKey, cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("读取主密钥失败: %w", err)
	}
	if generated {
		log.Printf("已生成主密钥 %s（权限 0600）。丢了它就解不开已保存的密钥；生产环境建议改用 REEVE_MASTER_KEY。", filepath.Join(cfg.DataDir, "master.key"))
	}
	vlt, err := vault.Open(cfg.DataDir, key)
	if err != nil {
		return nil, err
	}
	red := newRedactor(vlt, key, cfg.MasterKey)
	if modelOwner != nil {
		// 网关令牌是实例级的：主 Bot 签发的令牌，本 Bot 的外部任务输出里同样要抹掉。
		red.gateway = modelOwner.redactor.gateway
	}
	store.SetRedactor(red.RedactBytes)
	b := &Bot{cfg: cfg, store: store, vault: vlt, redactor: red, approver: builtin.NewQueueApprover(),
		execs: cli.NewRegistry(), kick: make(chan struct{}, 1),
		sch: sched{launched: map[string]bool{}, cliRuns: map[string]*cliTask{}}, modelOwner: modelOwner}
	b.profile.Store(profile)
	b.loadSkills()
	b.commands = commands.NewRegistry()
	commands.RegisterBuiltins(b.commands)
	if modelOwner != nil {
		b.slots = modelOwner.slots
	} else {
		b.slots = newTaskSlots(cfg.MaxConcurrent)
	}
	if modelOwner != nil {
		b.pool = modelOwner.pool
	} else if b.pool, err = openPool(cfg.DataDir, vlt); err != nil {
		return nil, fmt.Errorf("读取 API 渠道失败: %w", err)
	}
	if err := b.loadSettings(); err != nil {
		return nil, fmt.Errorf("读取模型设置失败: %w", err)
	}
	if err := b.loadExecutors(); err != nil {
		return nil, fmt.Errorf("读取执行器设置失败: %w", err)
	}

	guard, err := builtin.NewGuard(cfg.Rules, b.approver)
	if err != nil {
		return nil, err
	}
	guard.Store = approvalStore{store}
	guard.Secrets = guardedSecrets{vault: vlt, red: red}
	b.guard = guard
	resolveAgent := func() (builtin.Provider, string) { return b.resolveModel(false) }
	agent := &builtin.Agent{ResolveModel: resolveAgent, Guard: guard, Memory: b, Sink: builtin.SinkFunc(b.onAgentEvent),
		// 长任务的对话超过上限时，把旧的步骤压缩成摘要（先让模型写，失败就用机械摘要）。
		Compactor: &builtin.AutoCompactor{MaxTokens: cfg.ContextTokens, Summarize: builtin.SummarizeWith(resolveAgent)}}
	b.mgr = builtin.NewManager(agent, cfg.MaxConcurrent)
	b.chatG, _ = builtin.NewGuard(builtin.Rules{}, nil)
	b.chatG.Secrets = guardedSecrets{vault: vlt, red: red} // 聊天层工具的输出同样脱敏；直接引用密钥的只读调用会被拒绝
	b.rebuildChatTools()
	b.ctx, b.cancel = context.WithCancel(context.Background())
	b.recover()
	initialized = true
	return b, nil
}

func (b *Bot) Store() *Store { return b.store }

func (b *Bot) ID() string { return b.botProfile().ID }

// Start 启动聊天层和监工的后台循环。
func (b *Bot) Start() {
	b.wg.Add(2)
	go func() { defer b.wg.Done(); b.chatLoop() }()
	go func() { defer b.wg.Done(); b.watchStalls() }()
	b.schedule() // 重启前排着队的任务接着排
	b.wake()
}

func (b *Bot) stop() {
	b.shuttingDown.Store(true)
	b.cancel()
	// Wait for any dispatch that already passed the context check.
	b.sch.mu.Lock()
	b.sch.mu.Unlock()
	b.mgr.CancelAll()
}

func (b *Bot) Close() {
	b.stop()
	b.wg.Wait()
	b.mgr.Drain()
	b.background.Wait()
	b.store.Close()
}

// Track callbacks that may outlive a chat turn or agent task.
func (b *Bot) runBackground(fn func()) {
	b.background.Add(1)
	go func() {
		defer b.background.Done()
		fn()
	}()
}

func (b *Bot) wake() {
	select {
	case b.kick <- struct{}{}:
	default:
	}
}

// ---------- 用户输入 ----------

var stopWords = regexp.MustCompile(`(?i)^\s*(停|停止|停下|别做了|不要做了|取消|全部停止|stop|cancel)\s*[!！。.]*\s*$`)

func IsStop(text string) bool { return stopWords.MatchString(text) }

// Send 处理用户发来的一条消息。"停" 之类的消息由代码直接处理，不经过模型。
func (b *Bot) Send(text string, files ...Attachment) (Message, error) {
	text = strings.TrimSpace(text)
	if text == "" && len(files) == 0 {
		return Message{}, errors.New("消息为空")
	}
	if strings.HasPrefix(text, "/") {
		// 斜线命令在进聊天层之前处理：不建 message、不 wake、不进模型上下文。
		// 结果已经写成 command.result 事件；返回值只用于 HTTP 响应。
		if res, ok := b.CommandLine(context.Background(), text); ok {
			return Message{Role: "notice", Text: res.Text}, nil
		}
	}
	for i, f := range files { // 附件必须是工作区里真实存在的文件
		p, err := builtin.ResolvePath(b.cfg.Workspace, f.Path)
		if err != nil {
			return Message{}, err
		}
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			return Message{}, fmt.Errorf("附件 %s 不存在", f.Path)
		}
		files[i].Size = st.Size()
	}
	text, leaked := b.maskPastedKeys(text)
	text = b.redactText(text)
	m := Message{ID: builtin.NewID("msg"), Role: "user", Text: text, Files: files, CreatedAt: time.Now()}
	b.store.Append("message", m, false)
	if leaked {
		b.notify("用户刚才在聊天里粘贴了一段疑似密钥，系统已自动隐藏（你看不到）。请提醒用户不要在聊天里贴密钥，并用 request_secret 发一张安全卡片让他保存（起一个合适的 UPPER_SNAKE 名字）。")
	}
	if IsStop(text) {
		b.Stop(m.ID)
		return m, nil
	}
	b.wake()
	return m, nil
}

// Stop 立刻停止所有任务（运行中、排队中、暂停中）和正在进行的聊天回合，并回一句确定的话。
func (b *Bot) Stop(extraHandled ...string) int {
	n := b.stopAll(extraHandled...)
	reply := "好的，已经停下来了。"
	if n > 0 {
		reply = fmt.Sprintf("好的，已停止 %d 个任务。", n)
	}
	b.say(reply)
	return n
}

// stopAll 执行 Stop 的取消动作，但不写聊天回复（/stop 命令自己返回结果）。
func (b *Bot) stopAll(extraHandled ...string) int {
	// Serialize cancellation, dispatch tools and pending-turn registration.
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	b.turnGeneration++
	if b.turnCancel != nil {
		b.turnCancel(errStopped)
	}
	n := 0
	for _, t := range b.store.Tasks() {
		if t.Open() && b.CancelTask(t.ID, "user") == nil {
			n++
		}
	}
	ids := extraHandled
	for _, m := range b.store.Unhandled() { // 排队中的输入也一并作废
		ids = append(ids, m.ID)
	}
	if len(ids) > 0 {
		b.store.Append("message.handled", ids, true)
	}
	return n
}

// StopReply 只停掉聊天层正在写的这条回复（Esc），不动后台任务。
func (b *Bot) StopReply() bool {
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	if b.turnCancel == nil {
		return false
	}
	b.turnCancel(errReplyStopped)
	return true
}

func (b *Bot) say(text string) {
	b.store.Append("message", Message{ID: builtin.NewID("msg"), Role: "assistant", Text: text, CreatedAt: time.Now()}, false)
}

// ErrTaskEnded 表示审批所属的任务已经结束，不能再作答（HTTP 接口映射为 409）。
var ErrTaskEnded = errors.New("任务已结束，这个审批不能再作答")

// Decide 处理审批按钮。只有这个入口（用户点按钮 -> HTTP）能产生批准。
func (b *Bot) Decide(id, decision string) error {
	d := builtin.UserDecision{DecidedBy: "user"}
	switch decision {
	case "once":
		d.Approve, d.Scope = true, builtin.ScopeOnce
	case "session":
		d.Approve, d.Scope = true, builtin.ScopeSession
	case "deny":
	default:
		return fmt.Errorf("未知的决定: %s", decision)
	}
	a, known := b.store.Approval(id)
	if known {
		if t, ok := b.store.Task(a.TaskID); ok && terminal(t.Status) {
			return ErrTaskEnded
		}
		if a.Status != "pending" {
			return fmt.Errorf("这个审批已经处理过了")
		}
	}
	if err := b.approver.Resolve(id, d); err == nil {
		return nil
	}
	if !known {
		return fmt.Errorf("没有这个审批")
	}
	// 服务重启后任务停在安全点，没有活跃的等待者：把决定记下来，任务恢复时按记录生效。
	rec := builtin.ApprovalRecord{ID: id, TaskID: a.TaskID, ToolName: a.Tool, ArgsHash: builtin.ArgsHash(a.Args),
		Decision: builtin.Deny, Scope: builtin.ScopeOnce, CreatedAt: time.Now(), DecidedBy: "user"}
	if d.Approve {
		rec.Decision = builtin.Allow
		if d.Scope == builtin.ScopeSession {
			rec.Scope = builtin.ScopeSession
		}
	}
	b.guard.Grant(rec)
	b.store.Append("agent", builtin.Event{TaskID: a.TaskID, Type: builtin.EvApprovalDecided, Time: time.Now(),
		Data: map[string]any{"approval_id": id, "decision": string(rec.Decision), "scope": string(rec.Scope),
			"decided_by": "user", "reason": "服务重启后补记的决定"}}, false)
	_ = approvalStore{b.store}.SaveApproval(context.Background(), rec)
	if _, ok := b.store.Task(a.TaskID); !ok && rec.Decision == builtin.Allow {
		// 聊天层发起的操作没有可恢复的执行者：批准记下来了，但那次调用已经不可能再执行。
		b.notify("补记了你的批准，但那次操作没有执行（发起它的对话已结束）。请用一句话告诉用户这次操作没有执行，需要的话重新发起。")
	}
	return nil
}

// AnswerCard 处理选项卡片：选一个选项，或者关闭（= 不同意/不选择）。
func (b *Bot) AnswerCard(id, answer string, dismiss bool) error {
	c, ok := b.store.Card(id)
	if !ok {
		return errors.New("没有这张卡片")
	}
	if c.Status != "pending" {
		return errors.New("这张卡片已经处理过了")
	}
	if dismiss {
		c.Status = "dismissed"
		b.store.Append("card", c, false)
		b.notify(fmt.Sprintf("用户关闭了卡片「%s」，没有选择。视为不同意/不选择，不要再问同一个问题。", c.Question))
		return nil
	}
	if strings.TrimSpace(answer) == "" {
		return errors.New("答案为空")
	}
	c.Status, c.Answer = "answered", answer
	b.store.Append("card", c, false)
	b.store.Append("message", Message{ID: builtin.NewID("msg"), Role: "user", Text: answer, CreatedAt: time.Now()}, false)
	b.wake()
	return nil
}

// ---------- 记忆 ----------

func (b *Bot) memoryPath() string { return filepath.Join(b.cfg.DataDir, "memory.md") }

// Recall 实现 builtin.MemoryProvider。
func (b *Bot) Recall(_ context.Context, _, _ string) (string, error) {
	private, err := b.readMemory("private")
	if err != nil {
		return "", err
	}
	if b.coordinator == nil {
		return private, nil
	}
	shared, err := b.coordinator.readSharedMemory()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(shared) == "" {
		return private, nil
	}
	return "[共享记忆]\n" + shared + "\n[本 Bot 私有记忆]\n" + private, nil
}

func (b *Bot) readMemory(scope string) (string, error) {
	if scope == "" || scope == "private" {
		b.memoryMu.Lock()
		defer b.memoryMu.Unlock()
		data, err := os.ReadFile(b.memoryPath())
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return strings.TrimSpace(string(data)), err
	}
	if scope != "shared" {
		return "", errors.New("scope 必须是 private 或 shared")
	}
	if b.coordinator == nil {
		return "", errors.New("共享记忆仅在多 Bot 模式下可用")
	}
	return b.coordinator.readSharedMemory()
}

func (b *Bot) writeMemory(scope, text, mode string) error {
	if scope == "" || scope == "private" {
		b.memoryMu.Lock()
		defer b.memoryMu.Unlock()
		return b.writeMemoryFile(b.memoryPath(), text, mode)
	}
	if scope != "shared" {
		return errors.New("scope 必须是 private 或 shared")
	}
	if b.coordinator == nil {
		return errors.New("共享记忆仅在多 Bot 模式下可用")
	}
	return b.coordinator.writeSharedMemory(text, mode)
}

func (b *Bot) writeMemoryFile(path, text, mode string) error {
	if strings.TrimSpace(text) == "" || len(text) > 20000 {
		return errors.New("记忆内容不能为空且最多 20000 字节")
	}
	var content string
	if mode != "replace" {
		old, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		content = string(old)
	}
	if mode == "replace" {
		content = strings.TrimSpace(text) + "\n"
	} else {
		content = strings.TrimRight(content, "\n") + "\n- " + strings.TrimSpace(text) + "\n"
	}
	if len(content) > 1<<20 {
		return errors.New("记忆文件最多 1 MiB")
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".memory-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.WriteString(content)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// approvalStore 把审批记录写进事件日志（审批管线只需要这一个持久化接口）。
type approvalStore struct{ s *Store }

func (a approvalStore) SaveApproval(_ context.Context, r builtin.ApprovalRecord) error {
	a.s.Append("approval.record", r, true)
	return nil
}
