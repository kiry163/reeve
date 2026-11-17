package builtin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Policy 审批模式，参考 Codex。
type Policy string

const (
	PolicyUntrusted Policy = "untrusted"  // 只读自动放行，其余全部问用户
	PolicyOnRequest Policy = "on-request" // 工作区内读写、普通命令自动放行；危险操作问用户
	PolicyNever     Policy = "never"      // 全自动，从不问用户；除只读外一律拒绝
)

// ValidPolicy 判断策略是否是产品支持的取值。
func ValidPolicy(p Policy) bool {
	switch p {
	case PolicyUntrusted, PolicyOnRequest, PolicyNever:
		return true
	}
	return false
}

// Risk 风险等级。
type Risk int

const (
	RiskRead Risk = iota
	RiskWrite
	RiskExec
	RiskDanger
)

func (r Risk) String() string {
	return [...]string{"read", "write", "exec", "danger"}[r]
}

func (r Risk) MarshalJSON() ([]byte, error) { return json.Marshal(r.String()) }

func (r *Risk) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		var n int
		err = json.Unmarshal(b, &n)
		*r = Risk(n)
		return err
	}
	for i := RiskRead; i <= RiskDanger; i++ {
		if i.String() == s {
			*r = i
		}
	}
	return nil
}

type Decision string

const (
	Allow   Decision = "allow"
	Deny    Decision = "deny"
	AskUser Decision = "ask_user"
)

// Verdict 是一次审批判断。By 说明是谁做的决定：rule / policy / user / approval:<id>。
type Verdict struct {
	Decision Decision `json:"decision"`
	Reason   string   `json:"reason"`
	By       string   `json:"by,omitempty"`
}

type Scope string

const (
	ScopeOnce    Scope = "once"    // 只能用一次
	ScopeSession Scope = "session" // 本任务内，同样的参数可以反复用，任务结束时失效
)

// ApprovalRequest 是发给用户的审批请求（前端据此渲染卡片）。
// 审批没有时间上限：一直等，直到用户作答或任务结束。
type ApprovalRequest struct {
	ID        string          `json:"id"`
	TaskID    string          `json:"task_id"`
	BotID     string          `json:"bot_id,omitempty"`
	ToolName  string          `json:"tool_name"`
	Args      json.RawMessage `json:"args"`
	ArgsHash  string          `json:"args_hash"`
	Risk      Risk            `json:"risk"`
	Reason    string          `json:"reason"`
	CreatedAt time.Time       `json:"created_at"`
}

// UserDecision 是用户点按钮的结果。只能由后端在收到用户本人的点击后产生。
type UserDecision struct {
	Approve   bool   `json:"approve"`
	Scope     Scope  `json:"scope,omitempty"`
	DecidedBy string `json:"decided_by,omitempty"`
}

// Approver 把审批请求交给用户。必须阻塞到用户决定或 ctx 结束（任务取消/结束 = 拒绝）。
type Approver interface {
	RequestApproval(ctx context.Context, req ApprovalRequest) (UserDecision, error)
}

// ApprovalRecord 是后端保存的审批记录，绑定到具体任务 + 工具 + 参数哈希。
type ApprovalRecord struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	ToolName  string    `json:"tool_name"`
	ArgsHash  string    `json:"args_hash"`
	Decision  Decision  `json:"decision"`
	Scope     Scope     `json:"scope"`
	CreatedAt time.Time `json:"created_at"`
	DecidedBy string    `json:"decided_by"`
	Used      bool      `json:"used"`
}

// Matches 判断这条记录能否放行这次调用：任务与工具相同、参数哈希完全一致、once 的还没用过。
// 批准不按时间过期；session 批准在任务结束时由监工统一撤销。
func (r *ApprovalRecord) Matches(taskID, tool, hash string) bool {
	return r.Decision == Allow && r.TaskID == taskID && r.ToolName == tool &&
		r.ArgsHash == hash && !(r.Scope == ScopeOnce && r.Used)
}

// ArgsHash 对参数做规范化（键排序）后取 sha256。
func ArgsHash(args json.RawMessage) string {
	var v any
	canon := []byte(args)
	if json.Unmarshal(args, &v) == nil {
		canon, _ = json.Marshal(v)
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:])
}

// DefaultDanger 是默认的危险命令规则：命中后一律升级给用户（never 模式下直接拒绝）。
var DefaultDanger = []string{
	`\brm\s+(-\S*\s+)*-\S*[rR]`,           // 递归删除
	`\brm\s+(-\S+\s+)*(/|~|\$HOME)(\s|$)`, // 删根目录 / 家目录
	`\bsudo\b|\bsu\s|\bdoas\b`,
	`\b(curl|wget)\b.*\|\s*(ba|z)?sh\b`,                // curl | sh
	`\b(curl|wget|nc|ncat|scp|rsync|ssh|ftp|telnet)\b`, // 网络访问（可能外传数据）
	`\bmkfs|\bdd\s+if=|>\s*/dev/sd`,
	`:\(\)\s*\{`, // fork 炸弹
	`\bchmod\s+(-R\s+)?777|\bchown\s+-R`,
	`\b(shutdown|reboot|halt|poweroff)\b`,
	`\bgit\s+push\b|\bgit\s+reset\s+--hard`,
	`\.ssh/|id_rsa|\.aws/|\.env\b|\bprintenv\b`, // 读密钥
}

// Rules 是可配置的确定性规则（正则）。
type Rules struct {
	AllowShell  []string // 命中则直接放行（危险规则优先）
	DenyShell   []string // 命中则直接拒绝（最高优先）
	ExtraDanger []string // 追加的危险规则
	DenyTools   []string // 完全禁用的工具
}

// Guard 是每次工具调用执行前的分层审批：规则 -> 用户。
type Guard struct {
	Approver Approver
	Store    ApprovalRecorder // 可选：保存审批记录
	Now      func() time.Time
	Secrets  Secrets // 可选：密钥库，负责 {{secret:NAME}} 替换和输出脱敏

	denyTools           map[string]bool
	allow, deny, danger []*regexp.Regexp
	mu                  sync.Mutex
	records             []*ApprovalRecord
}

func NewGuard(r Rules, ap Approver) (*Guard, error) {
	g := &Guard{Approver: ap, denyTools: map[string]bool{}}
	for _, t := range r.DenyTools {
		g.denyTools[t] = true
	}
	var err error
	if g.allow, err = compileAll(r.AllowShell); err != nil {
		return nil, err
	}
	if g.deny, err = compileAll(r.DenyShell); err != nil {
		return nil, err
	}
	if g.danger, err = compileAll(append(append([]string{}, DefaultDanger...), r.ExtraDanger...)); err != nil {
		return nil, err
	}
	return g, nil
}

func compileAll(pats []string) ([]*regexp.Regexp, error) {
	var out []*regexp.Regexp
	for _, p := range pats {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("bad rule %q: %w", p, err)
		}
		out = append(out, re)
	}
	return out, nil
}

func anyMatch(res []*regexp.Regexp, s string) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

func (g *Guard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// Classify 只跑确定性规则和审批模式（不调模型、不问用户），返回判断和风险等级。
func (g *Guard) Classify(t Task, tool Tool, args json.RawMessage) (Verdict, Risk) {
	name := tool.Name()
	if g.denyTools[name] {
		return Verdict{Deny, "该工具已被禁用", "rule"}, RiskDanger
	}
	var m map[string]any
	_ = json.Unmarshal(args, &m)
	if p, ok := m["path"].(string); ok {
		if _, err := ResolvePath(t.Workspace, p); err != nil {
			return Verdict{Deny, err.Error(), "rule"}, RiskDanger
		}
	}
	risk := tool.Risk(args)
	cmd, isCmd := m["command"].(string)
	if isCmd && anyMatch(g.deny, cmd) {
		return Verdict{Deny, "命令命中拒绝名单", "rule"}, RiskDanger
	}
	if refs := SecretRefs(args); len(refs) > 0 && !handlesSecrets(tool) {
		// 用到密钥的调用一律要用户确认（任何策略下都一样）；policy=never 时 askUser 会直接拒绝。
		if g.Secrets == nil {
			return Verdict{Deny, "没有配置密钥库，不能引用 {{secret:…}}", "rule"}, risk
		}
		if err := g.Secrets.Allowed(tool.Name(), args, refs); err != nil {
			return Verdict{Deny, err.Error(), "rule"}, risk
		}
		if isCmd && anyMatch(g.danger, cmd) {
			risk = RiskDanger
		}
		risk = max(risk, RiskExec)
		if t.Policy == PolicyNever {
			return Verdict{Deny, "要用密钥，但当前模式不询问用户", "policy"}, risk
		}
		return Verdict{AskUser, "要使用密钥 " + strings.Join(refs, "、") + "（真实值只在执行时替换，不会给模型看）", "rule"}, risk
	}
	if isCmd {
		switch {
		case anyMatch(g.danger, cmd):
			risk = RiskDanger
		case anyMatch(g.allow, cmd):
			return Verdict{Allow, "命令命中允许名单", "rule"}, risk
		}
	}
	if risk == RiskRead {
		return Verdict{Allow, "工作区内只读操作", "rule"}, risk
	}
	switch t.Policy {
	case PolicyNever:
		// 全自动但从不询问用户：除了只读操作，一律拒绝（不弹卡）。
		return Verdict{Deny, "当前模式不询问用户，非只读操作一律拒绝", "policy"}, risk
	case PolicyOnRequest:
		if risk == RiskDanger {
			return Verdict{AskUser, "危险操作需要用户确认", "policy"}, risk
		}
		return Verdict{Allow, "工作区内的普通操作", "policy"}, risk
	default: // untrusted、空值以及未知取值都走最保守的一档
		return Verdict{AskUser, "当前模式下，改文件和执行命令都要先经过你确认", "policy"}, risk
	}
}

// Grant 添加一条审批记录。
func (g *Guard) Grant(r ApprovalRecord) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, existing := range g.records {
		if existing.ID == r.ID { // 同一条记录（例如重启后补记的决定）只保留一份
			return
		}
	}
	g.records = append(g.records, &r)
}

// Restore 在启动时把持久化的批准记录注入审批管线。已用过的 once 记录也一并恢复：
// Used 只用于保持记录状态一致，放行判定始终由 Matches 拦掉已用的 once。
func (g *Guard) Restore(recs []ApprovalRecord) {
	for _, r := range recs {
		if r.Decision == Allow {
			g.Grant(r)
		}
	}
}

// Revoke 撤销某个任务的全部批准记录（任务结束时调用，session 批准不会跨任务残留）。
func (g *Guard) Revoke(taskID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	kept := g.records[:0]
	for _, r := range g.records {
		if r.TaskID != taskID {
			kept = append(kept, r)
		}
	}
	g.records = kept
}

// Consume 查找能放行这次调用的记录；once 记录会被标记为已用。
func (g *Guard) Consume(taskID, tool, hash string) (ApprovalRecord, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, r := range g.records {
		if r.Matches(taskID, tool, hash) {
			if r.Scope == ScopeOnce {
				r.Used = true
				if g.Store != nil { // 必须落盘：否则重启后这条 once 批准又能再用一次
					_ = g.Store.SaveApproval(context.Background(), *r)
				}
			}
			return *r, true
		}
	}
	return ApprovalRecord{}, false
}

// Check 是完整的审批流程，在工具执行前调用。emit 用来发出审批事件。
func (g *Guard) Check(ctx context.Context, t Task, tool Tool, args json.RawMessage, emit func(EventType, any)) Verdict {
	v, risk := g.Classify(t, tool, args)
	if v.Decision == Allow || v.Decision == Deny {
		return v
	}
	hash := ArgsHash(args)
	if rec, ok := g.Consume(t.ID, tool.Name(), hash); ok { // 之前批过（session 范围）
		return Verdict{Allow, "已有用户批准记录", "approval:" + rec.ID}
	}
	return g.askUser(ctx, t, tool.Name(), args, hash, risk, v.Reason, emit)
}

func (g *Guard) askUser(ctx context.Context, t Task, tool string, args json.RawMessage, hash string, risk Risk, reason string, emit func(EventType, any)) Verdict {
	if t.Policy == PolicyNever || g.Approver == nil {
		return Verdict{Deny, reason + "（无法询问用户）", "policy"}
	}
	now := g.now()
	req := ApprovalRequest{ID: NewID("appr"), TaskID: t.ID, BotID: t.BotID, ToolName: tool, Args: args,
		ArgsHash: hash, Risk: risk, Reason: reason, CreatedAt: now}
	var d UserDecision
	var err error
	if ra, ok := g.Approver.(registeringApprover); ok {
		// 先登记再通知界面：否则用户点得很快时，决定会比登记先到，被当成「不存在的审批」拒掉。
		wait := ra.Register(req)
		emit(EvApprovalRequested, req)
		d, err = wait(ctx) // 没有时间上限：一直等到用户作答或任务结束
	} else {
		emit(EvApprovalRequested, req)
		d, err = g.Approver.RequestApproval(ctx, req) // 阻塞等待，不消耗 token
	}
	rec := ApprovalRecord{ID: req.ID, TaskID: t.ID, ToolName: tool, ArgsHash: hash, Decision: Deny,
		Scope: ScopeOnce, CreatedAt: g.now(), DecidedBy: d.DecidedBy}
	verdict := Verdict{Deny, "用户拒绝了这个操作", "user"}
	switch {
	case err != nil:
		verdict.Reason = "任务已结束或取消，审批没有再做"
		rec.DecidedBy = "cancelled"
	case d.Approve:
		rec.Decision = Allow
		if d.Scope == ScopeSession {
			rec.Scope = ScopeSession
		}
	}
	if rec.DecidedBy == "" {
		rec.DecidedBy = "user"
	}
	g.Grant(rec)
	if g.Store != nil {
		_ = g.Store.SaveApproval(context.WithoutCancel(ctx), rec)
	}
	if rec.Decision == Allow {
		if r, ok := g.Consume(t.ID, tool, hash); ok { // 用记录本身去放行，保证哈希绑定
			verdict = Verdict{Allow, "用户已批准", "approval:" + r.ID}
		}
	}
	emit(EvApprovalDecided, map[string]any{"approval_id": req.ID, "decision": verdict.Decision,
		"scope": rec.Scope, "decided_by": rec.DecidedBy, "reason": verdict.Reason})
	return verdict
}

// DeniedMessage 是被拒绝时返回给模型的工具结果。
func DeniedMessage(v Verdict) string {
	return fmt.Sprintf("DENIED (%s): %s\n这个操作没有被批准。不要重试，也不要换别的方法达到同样效果；"+
		"请向用户说明情况，然后继续做其他部分或结束任务。", v.By, strings.TrimSpace(v.Reason))
}

// QueueApprover 把审批请求挂起，等外部（例如 HTTP 接口收到用户点击）调用 Resolve。
type QueueApprover struct {
	mu      sync.Mutex
	pending map[string]pendingApproval
}

type pendingApproval struct {
	req ApprovalRequest
	ch  chan UserDecision
}

func NewQueueApprover() *QueueApprover {
	return &QueueApprover{pending: map[string]pendingApproval{}}
}

// registeringApprover 能把登记和等待分开：Guard 先登记、再发出审批事件、最后等待。
type registeringApprover interface {
	Register(req ApprovalRequest) (wait func(ctx context.Context) (UserDecision, error))
}

func (q *QueueApprover) RequestApproval(ctx context.Context, req ApprovalRequest) (UserDecision, error) {
	return q.Register(req)(ctx)
}

// Register 立即登记审批（Resolve 从这一刻起就能找到它），返回的 wait 阻塞到用户决定或 ctx 结束。
func (q *QueueApprover) Register(req ApprovalRequest) func(ctx context.Context) (UserDecision, error) {
	p := pendingApproval{req, make(chan UserDecision, 1)}
	q.mu.Lock()
	q.pending[req.ID] = p
	q.mu.Unlock()
	return func(ctx context.Context) (UserDecision, error) {
		defer func() { q.mu.Lock(); delete(q.pending, req.ID); q.mu.Unlock() }()
		select {
		case d := <-p.ch:
			return d, nil
		case <-ctx.Done():
			return UserDecision{}, ctx.Err()
		}
	}
}

// Resolve 提交用户的决定。只应在后端确认是用户本人点击后调用。
func (q *QueueApprover) Resolve(id string, d UserDecision) error {
	q.mu.Lock()
	p, ok := q.pending[id]
	delete(q.pending, id)
	q.mu.Unlock()
	if !ok {
		return fmt.Errorf("approval %s not pending (expired or already decided)", id)
	}
	p.ch <- d
	return nil
}

func (q *QueueApprover) Pending() []ApprovalRequest {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]ApprovalRequest, 0, len(q.pending))
	for _, p := range q.pending {
		out = append(out, p.req)
	}
	return out
}
