package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kiry163/reeve/internal/builtin"
	"github.com/kiry163/reeve/internal/vault"
)

// ntfy 推送：服务端只负责把「需要人看的事件」变成一条极简通知，正文和工具参数都不出本机。
// 传输走用户自建的 ntfy；手机端用 ntfy App 接收，点击通过 reeve://open 回到对应聊天。

const (
	pushKindApproval = "approval"
	pushKindTask     = "task"
	pushKindReply    = "reply"
	pushKindTest     = "test"
)

// ntfy 的发布认证：匿名、Access token（Bearer）、用户名密码（Basic）。
// ntfy 官方 CLI 也要求 token 和用户名密码二选一，不能同时使用。
const (
	pushAuthNone  = "none"
	pushAuthToken = "token"
	pushAuthBasic = "basic"
)

var pushTopicRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// pushConfig 是实例级推送配置；token 和密码不在这里，单独密封存储。
type pushConfig struct {
	Enabled    bool   `json:"enabled"`
	URL        string `json:"url"`
	Topic      string `json:"topic"`
	PublicHost string `json:"public_host,omitempty"`
	AuthMethod string `json:"auth_method,omitempty"`
	Username   string `json:"username,omitempty"`
	Approval   bool   `json:"approval"`
	Task       bool   `json:"task"`
	Reply      bool   `json:"reply"`
}

type pushFile struct {
	pushConfig
	SealedToken    string `json:"sealed_token,omitempty"`
	SealedPassword string `json:"sealed_password,omitempty"`
}

// pushDraft 是 Web 设置页提交的形状。Token/Password 为 nil 表示保留旧值，空串表示清除。
// AuthMethod 为空的旧客户端按现有凭据推断，保持兼容。
type pushDraft struct {
	Enabled    bool    `json:"enabled"`
	URL        string  `json:"url"`
	Topic      string  `json:"topic"`
	PublicHost string  `json:"public_host"`
	AuthMethod string  `json:"auth_method"`
	Username   string  `json:"username"`
	Approval   bool    `json:"approval"`
	Task       bool    `json:"task"`
	Reply      bool    `json:"reply"`
	Token      *string `json:"token"`
	Password   *string `json:"password"`
}

// pushView 是给前端的只读视图；永远不回显 token 和密码本身。
type pushView struct {
	Enabled     bool   `json:"enabled"`
	URL         string `json:"url"`
	Topic       string `json:"topic"`
	PublicHost  string `json:"public_host"`
	AuthMethod  string `json:"auth_method"`
	Username    string `json:"username"`
	Approval    bool   `json:"approval"`
	Task        bool   `json:"task"`
	Reply       bool   `json:"reply"`
	HasToken    bool   `json:"has_token"`
	HasPassword bool   `json:"has_password"`
}

type pushNotice struct {
	Kind     string
	Title    string
	Message  string
	Click    string
	Priority int
	Tags     []string
}

// pushCredentials 是解密后的认证凭据；只在内存里，按 AuthMethod 选用其中一种。
type pushCredentials struct {
	Token    string
	Password string
}

// pushSender 用来隔离 ntfy 传输，测试里可以换成 fake。
type pushSender interface {
	Send(ctx context.Context, cfg pushConfig, cred pushCredentials, n pushNotice) error
}

type pushManager struct {
	dir   string
	path  string
	vault *vault.Vault

	mu             sync.Mutex
	cfg            pushConfig
	token          string
	password       string
	sealed         string
	sealedPassword string
	closed         bool

	sender pushSender

	ctx    context.Context
	cancel context.CancelFunc
	stop   chan struct{}
	sendCh chan pushNotice
	wg     sync.WaitGroup

	attachments map[string]context.CancelFunc

	termMu   sync.Mutex
	terminal map[string]time.Time

	replyMu     sync.Mutex
	replyTimers map[string]*time.Timer
	replyDelay  time.Duration

	dropMu      sync.Mutex
	lastDropLog time.Time
}

func defaultPushConfig() pushConfig {
	return pushConfig{AuthMethod: pushAuthNone, Approval: true, Task: true, Reply: true}
}

func newPushManager(dir string, v *vault.Vault) *pushManager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &pushManager{
		dir:         dir,
		path:        filepath.Join(dir, "push.json"),
		vault:       v,
		cfg:         defaultPushConfig(),
		sender:      &ntfySender{client: &http.Client{Timeout: 8 * time.Second}},
		ctx:         ctx,
		cancel:      cancel,
		stop:        make(chan struct{}),
		sendCh:      make(chan pushNotice, 64),
		attachments: map[string]context.CancelFunc{},
		terminal:    map[string]time.Time{},
		replyTimers: map[string]*time.Timer{},
		replyDelay:  3 * time.Second,
	}
	m.load()
	m.wg.Add(1)
	go m.sendLoop()
	return m
}

// load 读取 push.json；文件不存在或损坏时用默认值启动，不阻塞服务。
func (m *pushManager) load() {
	m.cfg = defaultPushConfig()
	raw, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		log.Printf("读取 %s 失败：%v（推送保持关闭）", filepath.Base(m.path), err)
		return
	}
	// 默认开启三类提醒；auth_method 故意留空，旧文件里没有这个字段时才能推断。
	def := defaultPushConfig()
	def.AuthMethod = ""
	f := pushFile{pushConfig: def}
	if err := json.Unmarshal(raw, &f); err != nil {
		log.Printf("解析 %s 失败：%v（推送保持关闭）", filepath.Base(m.path), err)
		return
	}
	m.cfg = f.pushConfig
	if raw := strings.ToLower(strings.TrimSpace(m.cfg.AuthMethod)); raw != "" && !validPushAuth(raw) {
		log.Printf("未知的推送认证方式 %q，按已保存的凭据推断", raw)
	}
	// 旧版（0.1.0）没有 auth_method：有 token 就是 token，否则没有认证。
	m.cfg.AuthMethod = inferPushAuthMethod(m.cfg.AuthMethod, m.cfg.Username, f.SealedToken, f.SealedPassword)
	m.sealed = f.SealedToken
	m.sealedPassword = f.SealedPassword
	if f.SealedToken != "" {
		if token, err := m.vault.OpenAPIKey(f.SealedToken); err != nil {
			log.Printf("推送 token 无法解密（主密钥可能已更换），请在设置里重新填写")
		} else {
			m.token = token
		}
	}
	if f.SealedPassword != "" {
		if password, err := m.vault.OpenAPIKey(f.SealedPassword); err != nil {
			log.Printf("推送密码无法解密（主密钥可能已更换），请在设置里重新填写")
		} else {
			m.password = password
		}
	}
}

func validPushAuth(method string) bool {
	switch method {
	case pushAuthNone, pushAuthToken, pushAuthBasic:
		return true
	}
	return false
}

// inferPushAuthMethod 兼容旧配置：没有 auth_method 时按已保存的凭据推断。
func inferPushAuthMethod(method, username, sealedToken, sealedPassword string) string {
	method = strings.ToLower(strings.TrimSpace(method))
	if validPushAuth(method) {
		return method
	}
	switch {
	case sealedToken != "":
		return pushAuthToken
	case username != "" || sealedPassword != "":
		return pushAuthBasic
	default:
		return pushAuthNone
	}
}

func (m *pushManager) view() pushView {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.viewLocked()
}

func (m *pushManager) viewLocked() pushView {
	return pushView{
		Enabled:     m.cfg.Enabled,
		URL:         m.cfg.URL,
		Topic:       m.cfg.Topic,
		PublicHost:  m.cfg.PublicHost,
		AuthMethod:  m.cfg.AuthMethod,
		Username:    m.cfg.Username,
		Approval:    m.cfg.Approval,
		Task:        m.cfg.Task,
		Reply:       m.cfg.Reply,
		HasToken:    m.token != "",
		HasPassword: m.password != "",
	}
}

// update 校验并保存设置。Token/Password 为 nil 时保留旧值，空串时清除。
func (m *pushManager) update(d pushDraft) (pushView, error) {
	cfg, err := normalizePushDraft(d)
	if err != nil {
		return pushView{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// 旧客户端不带 auth_method：提交了新凭据时按凭据切换；没动凭据就沿用现有方式。
	if cfg.AuthMethod == "" {
		switch {
		case d.Token != nil && normalizePushToken(*d.Token) != "":
			cfg.AuthMethod = pushAuthToken
		case d.Token != nil:
			// 显式清空 token：没有其他凭据时退回匿名，保持旧行为。
			if strings.TrimSpace(d.Username) != "" {
				cfg.AuthMethod = pushAuthBasic
			} else {
				cfg.AuthMethod = pushAuthNone
			}
		case strings.TrimSpace(d.Username) != "":
			cfg.AuthMethod = pushAuthBasic
		case m.cfg.AuthMethod != "":
			cfg.AuthMethod = m.cfg.AuthMethod
		default:
			cfg.AuthMethod = pushAuthNone
		}
	}

	sealed, token := m.sealed, m.token
	if d.Token != nil {
		plain := normalizePushToken(*d.Token)
		if strings.ContainsAny(plain, " \t\r\n") {
			return pushView{}, errors.New("访问 token 不能包含空白字符")
		}
		if plain == "" {
			sealed, token = "", ""
		} else {
			sealed, err = m.vault.SealAPIKey(plain)
			if err != nil {
				return pushView{}, fmt.Errorf("保存 token 失败：%w", err)
			}
			token = plain
		}
	}
	sealedPassword, password := m.sealedPassword, m.password
	if d.Password != nil {
		// 密码可以包含空格或冒号，这里只按空串判断清除。
		if *d.Password == "" {
			sealedPassword, password = "", ""
		} else {
			sealedPassword, err = m.vault.SealAPIKey(*d.Password)
			if err != nil {
				return pushView{}, fmt.Errorf("保存密码失败：%w", err)
			}
			password = *d.Password
		}
	}

	// 认证方式决定哪些凭据生效；不用的凭据一并清掉，避免旧 token 悄悄留着。
	switch cfg.AuthMethod {
	case pushAuthNone:
		cfg.Username = ""
		sealed, token, sealedPassword, password = "", "", "", ""
	case pushAuthToken:
		if token == "" {
			return pushView{}, errors.New("使用访问 token 认证前请填写 token")
		}
		cfg.Username = ""
		sealedPassword, password = "", ""
	case pushAuthBasic:
		if cfg.Username == "" {
			return pushView{}, errors.New("使用用户名密码认证前请填写用户名")
		}
		if password == "" {
			return pushView{}, errors.New("使用用户名密码认证前请填写密码")
		}
		sealed, token = "", ""
	default:
		return pushView{}, errors.New("未知的认证方式")
	}

	f := pushFile{pushConfig: cfg, SealedToken: sealed, SealedPassword: sealedPassword}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return pushView{}, err
	}
	if err := writePrivate(m.dir, m.path, raw); err != nil {
		return pushView{}, err
	}
	m.cfg, m.sealed, m.token, m.sealedPassword, m.password = cfg, sealed, token, sealedPassword, password
	return m.viewLocked(), nil
}

// normalizePushToken 接受直接粘贴的 token，也接受整段 "Bearer <token>"。
func normalizePushToken(s string) string {
	s = strings.TrimSpace(s)
	const prefix = "Bearer "
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		s = strings.TrimSpace(s[len(prefix):])
	}
	return s
}

func normalizePushDraft(d pushDraft) (pushConfig, error) {
	cfg := pushConfig{
		Enabled:    d.Enabled,
		URL:        strings.TrimSpace(d.URL),
		Topic:      strings.TrimSpace(d.Topic),
		PublicHost: strings.TrimSpace(d.PublicHost),
		AuthMethod: strings.ToLower(strings.TrimSpace(d.AuthMethod)),
		Username:   strings.TrimSpace(d.Username),
		Approval:   d.Approval,
		Task:       d.Task,
		Reply:      d.Reply,
	}
	switch cfg.AuthMethod {
	case "", pushAuthNone, pushAuthToken, pushAuthBasic:
	default:
		return pushConfig{}, errors.New("认证方式只能是 none、token 或 basic")
	}
	if strings.ContainsAny(cfg.Username, ":\r\n\t") {
		return pushConfig{}, errors.New("用户名不能包含冒号或控制字符")
	}
	if cfg.URL != "" {
		u, err := url.Parse(cfg.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			return pushConfig{}, errors.New("ntfy 地址要是 http(s):// 开头的完整地址")
		}
		if u.RawQuery != "" || u.Fragment != "" || strings.TrimSuffix(u.Path, "/") != "" {
			return pushConfig{}, errors.New("ntfy 地址只填到域名，不要带路径、查询或片段")
		}
		cfg.URL = strings.TrimRight(u.Scheme+"://"+u.Host, "/")
	}
	if cfg.Topic != "" && !pushTopicRe.MatchString(cfg.Topic) {
		return pushConfig{}, errors.New("ntfy 主题只能包含字母、数字、下划线和连字符，长度 1–64")
	}
	if cfg.PublicHost != "" {
		host := cfg.PublicHost
		if strings.Contains(host, "://") {
			u, err := url.Parse(host)
			if err != nil || u.Host == "" {
				return pushConfig{}, errors.New("public_host 无效")
			}
			host = u.Host
		}
		if strings.ContainsAny(host, " \t\r\n/?#") || strings.Contains(host, "@") {
			return pushConfig{}, errors.New("public_host 只填 host[:port]，不要带路径或协议之外的内容")
		}
		cfg.PublicHost = host
	}
	if cfg.Enabled {
		if cfg.URL == "" {
			return pushConfig{}, errors.New("启用推送前请填写 ntfy 地址")
		}
		if cfg.Topic == "" {
			return pushConfig{}, errors.New("启用推送前请填写 ntfy 主题")
		}
	}
	return cfg, nil
}

func (m *pushManager) test(ctx context.Context) error {
	m.mu.Lock()
	cfg, cred := m.cfg, pushCredentials{Token: m.token, Password: m.password}
	m.mu.Unlock()
	if cfg.URL == "" || cfg.Topic == "" {
		return errors.New("先填写 ntfy 地址和主题")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return m.sender.Send(ctx, cfg, cred, pushNotice{
		Kind: pushKindTest, Title: "reeve", Message: "reeve 推送测试", Click: "reeve://open", Priority: 3,
	})
}

// attach 给一个 Bot 挂事件消费者。调用方保证同一个 Bot 只挂一次。
func (m *pushManager) attach(b *Bot) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	if _, ok := m.attachments[b.ID()]; ok {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(b.ctx)
	m.attachments[b.ID()] = cancel
	m.wg.Add(1)
	m.mu.Unlock()

	go func() {
		defer m.wg.Done()
		m.consume(ctx, b)
		m.mu.Lock()
		delete(m.attachments, b.ID())
		m.mu.Unlock()
	}()
}

func (m *pushManager) close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.mu.Unlock()

	m.cancel()
	close(m.stop)
	m.mu.Lock()
	for _, cancel := range m.attachments {
		cancel()
	}
	clear(m.attachments)
	m.mu.Unlock()
	m.replyMu.Lock()
	for _, t := range m.replyTimers {
		t.Stop()
	}
	clear(m.replyTimers)
	m.replyMu.Unlock()
	m.wg.Wait()
}

// consume 只做解析和入队，网络发送在 sendLoop 里；订阅被慢消费者断开时按 seq 补发。
func (m *pushManager) consume(ctx context.Context, b *Bot) {
	last := b.store.LastSeq()
	for {
		live, unsub := b.store.Subscribe()
		for _, e := range b.store.Since(last) {
			if e.Seq > last {
				last = e.Seq
				m.handleEvent(b, e)
			}
		}
		reconnect := false
		for !reconnect {
			select {
			case e, ok := <-live:
				if !ok {
					unsub()
					reconnect = true
					continue
				}
				if e.Seq > 0 && e.Seq > last {
					last = e.Seq
					m.handleEvent(b, e)
				}
			case <-ctx.Done():
				unsub()
				return
			case <-m.stop:
				unsub()
				return
			}
		}
	}
}

type pushAgentEvent struct {
	Type builtin.EventType `json:"type"`
	Data json.RawMessage   `json:"data"`
}

func (m *pushManager) handleEvent(b *Bot, e Event) {
	if e.Internal || e.Seq <= 0 {
		return
	}
	switch e.Kind {
	case "message":
		var msg Message
		if json.Unmarshal(e.Data, &msg) != nil {
			return
		}
		if msg.Role != "assistant" || msg.Silent || strings.TrimSpace(msg.Text) == "" {
			return
		}
		m.scheduleReply(b, msg)
	case "agent":
		var env pushAgentEvent
		if json.Unmarshal(e.Data, &env) != nil {
			return
		}
		switch env.Type {
		case builtin.EvApprovalRequested:
			var req builtin.ApprovalRequest
			if json.Unmarshal(env.Data, &req) != nil {
				return
			}
			m.notifyApproval(b, req)
		case builtin.EvTaskCompleted, builtin.EvTaskFailed, builtin.EvTaskCancelled:
			var res builtin.Result
			if json.Unmarshal(env.Data, &res) != nil {
				return
			}
			status := string(res.Status)
			if status == "" {
				status = map[builtin.EventType]string{
					builtin.EvTaskCompleted: "completed",
					builtin.EvTaskFailed:    "failed",
					builtin.EvTaskCancelled: "cancelled",
				}[env.Type]
			}
			m.notifyTask(b, res.TaskID, status)
		}
	case "task.updated":
		var p struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if json.Unmarshal(e.Data, &p) == nil && terminal(p.Status) {
			m.notifyTask(b, p.ID, p.Status)
		}
	}
}

func (m *pushManager) enabledFor(kind string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.Enabled && pushKindEnabled(m.cfg, kind)
}

func (m *pushManager) notifyApproval(b *Bot, req builtin.ApprovalRequest) {
	if !m.enabledFor(pushKindApproval) {
		return
	}
	msg := "有新的审批请求"
	if tool := strings.TrimSpace(req.ToolName); tool != "" {
		msg += "：" + tool
	}
	m.enqueue(pushNotice{
		Kind: pushKindApproval, Title: m.title(b),
		Message: msg, Click: m.clickURL(b.ID()), Priority: 4, Tags: []string{"warning"},
	})
}

func (m *pushManager) notifyTask(b *Bot, taskID, status string) {
	if taskID == "" || !terminal(status) || !m.enabledFor(pushKindTask) {
		return
	}
	// chat_... 是聊天层内部任务，没有 TaskRec，不打扰用户。
	t, ok := b.store.Task(taskID)
	if !ok {
		return
	}
	if m.seenTerminal(b.ID(), taskID, status) {
		return
	}
	verb, tag := "完成了", "white_check_mark"
	switch status {
	case "failed":
		verb, tag = "失败了", "x"
	case "cancelled":
		verb, tag = "已取消", "stop_sign"
	}
	msg := "任务" + verb
	if title := strings.TrimSpace(t.Title); title != "" {
		msg = fmt.Sprintf("任务「%s」%s", title, verb)
	}
	m.enqueue(pushNotice{
		Kind: pushKindTask, Title: m.title(b),
		Message: msg, Click: m.clickURL(b.ID()), Priority: 3, Tags: []string{tag},
	})
}

func (m *pushManager) scheduleReply(b *Bot, msg Message) {
	if !m.enabledFor(pushKindReply) {
		return
	}
	m.replyMu.Lock()
	if t := m.replyTimers[b.ID()]; t != nil {
		t.Stop()
	}
	m.replyTimers[b.ID()] = time.AfterFunc(m.replyDelay, func() {
		m.replyMu.Lock()
		delete(m.replyTimers, b.ID())
		m.replyMu.Unlock()
		m.mu.Lock()
		closed := m.closed
		m.mu.Unlock()
		if closed || b.ctx.Err() != nil {
			return
		}
		stored, ok := b.store.Message(msg.ID)
		if !ok || stored.Hidden {
			return
		}
		m.enqueue(pushNotice{
			Kind: pushKindReply, Title: m.title(b),
			Message: "助手有新回复", Click: m.clickURL(b.ID()), Priority: 2, Tags: []string{"speech_balloon"},
		})
	})
	m.replyMu.Unlock()
}

func (m *pushManager) enqueue(n pushNotice) {
	select {
	case m.sendCh <- n:
	default:
		m.dropMu.Lock()
		now := time.Now()
		shouldLog := now.Sub(m.lastDropLog) >= time.Minute
		if shouldLog {
			m.lastDropLog = now
		}
		m.dropMu.Unlock()
		if shouldLog {
			log.Printf("推送队列已满，丢弃一条 %s 通知", n.Kind)
		}
	}
}

func (m *pushManager) sendLoop() {
	defer m.wg.Done()
	for {
		select {
		case <-m.stop:
			return
		case n := <-m.sendCh:
			m.mu.Lock()
			cfg, cred := m.cfg, pushCredentials{Token: m.token, Password: m.password}
			m.mu.Unlock()
			if !cfg.Enabled || !pushKindEnabled(cfg, n.Kind) {
				continue
			}
			ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
			err := m.sender.Send(ctx, cfg, cred, n)
			cancel()
			if err != nil {
				log.Printf("ntfy 推送失败（%s）：%v", n.Kind, err)
			}
		}
	}
}

func pushKindEnabled(cfg pushConfig, kind string) bool {
	switch kind {
	case pushKindApproval:
		return cfg.Approval
	case pushKindTask:
		return cfg.Task
	case pushKindReply:
		return cfg.Reply
	case pushKindTest:
		return true
	}
	return false
}

func (m *pushManager) botLabel(b *Bot) string {
	if name := strings.TrimSpace(b.botProfile().Name); name != "" {
		return name
	}
	if name := strings.TrimSpace(b.cfg.Name); name != "" {
		return name
	}
	return "reeve"
}

func (m *pushManager) title(b *Bot) string {
	if label := m.botLabel(b); label != "reeve" {
		return "reeve · " + label
	}
	return "reeve"
}

func (m *pushManager) clickURL(botID string) string {
	m.mu.Lock()
	host := m.cfg.PublicHost
	m.mu.Unlock()
	q := url.Values{}
	q.Set("bot", botID)
	if host != "" {
		q.Set("host", host)
	}
	return "reeve://open?" + q.Encode()
}

func (m *pushManager) seenTerminal(botID, taskID, status string) bool {
	key := botID + "|" + taskID + "|" + status
	now := time.Now()
	m.termMu.Lock()
	defer m.termMu.Unlock()
	if _, ok := m.terminal[key]; ok {
		return true
	}
	m.terminal[key] = now
	if len(m.terminal) > 2048 {
		for k, t := range m.terminal {
			if now.Sub(t) > 10*time.Minute {
				delete(m.terminal, k)
			}
		}
	}
	return false
}

// ntfySender 直接调用 ntfy 的 JSON publish 接口。
type ntfySender struct{ client *http.Client }

type ntfyPayload struct {
	Topic    string   `json:"topic"`
	Title    string   `json:"title,omitempty"`
	Message  string   `json:"message"`
	Click    string   `json:"click,omitempty"`
	Priority int      `json:"priority,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

func (s *ntfySender) Send(ctx context.Context, cfg pushConfig, cred pushCredentials, n pushNotice) error {
	body, err := json.Marshal(ntfyPayload{
		Topic: cfg.Topic, Title: n.Title, Message: n.Message, Click: n.Click,
		Priority: n.Priority, Tags: n.Tags,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.URL, "/")+"/", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "reeve")
	method := cfg.AuthMethod
	if method == "" {
		switch {
		case cred.Token != "":
			method = pushAuthToken
		case cfg.Username != "":
			method = pushAuthBasic
		default:
			method = pushAuthNone
		}
	}
	switch method {
	case pushAuthNone:
		// 匿名发布；topic 本身当作密码用。
	case pushAuthToken:
		if cred.Token == "" {
			return errors.New("缺少访问 token")
		}
		req.Header.Set("Authorization", "Bearer "+cred.Token)
	case pushAuthBasic:
		if cfg.Username == "" || cred.Password == "" {
			return errors.New("缺少用户名或密码")
		}
		req.SetBasicAuth(cfg.Username, cred.Password)
	default:
		return errors.New("未知的认证方式")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	msg := strings.TrimSpace(string(raw))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		if msg != "" {
			msg += "；"
		}
		msg += "检查认证方式、token 或用户名密码，以及 topic 的读写权限"
	}
	return fmt.Errorf("ntfy 返回 %d：%s", resp.StatusCode, msg)
}
