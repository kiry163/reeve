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

var pushTopicRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// pushConfig 是实例级推送配置；token 不在这里，单独密封存储。
type pushConfig struct {
	Enabled    bool   `json:"enabled"`
	URL        string `json:"url"`
	Topic      string `json:"topic"`
	PublicHost string `json:"public_host,omitempty"`
	Approval   bool   `json:"approval"`
	Task       bool   `json:"task"`
	Reply      bool   `json:"reply"`
}

type pushFile struct {
	pushConfig
	SealedToken string `json:"sealed_token,omitempty"`
}

// pushDraft 是 Web 设置页提交的形状。Token 为 nil 表示保留旧 token，空串表示清除。
type pushDraft struct {
	Enabled    bool    `json:"enabled"`
	URL        string  `json:"url"`
	Topic      string  `json:"topic"`
	PublicHost string  `json:"public_host"`
	Approval   bool    `json:"approval"`
	Task       bool    `json:"task"`
	Reply      bool    `json:"reply"`
	Token      *string `json:"token"`
}

// pushView 是给前端的只读视图；永远不回显 token 本身。
type pushView struct {
	Enabled    bool   `json:"enabled"`
	URL        string `json:"url"`
	Topic      string `json:"topic"`
	PublicHost string `json:"public_host"`
	Approval   bool   `json:"approval"`
	Task       bool   `json:"task"`
	Reply      bool   `json:"reply"`
	HasToken   bool   `json:"has_token"`
}

type pushNotice struct {
	Kind     string
	Title    string
	Message  string
	Click    string
	Priority int
	Tags     []string
}

// pushSender 用来隔离 ntfy 传输，测试里可以换成 fake。
type pushSender interface {
	Send(ctx context.Context, cfg pushConfig, token string, n pushNotice) error
}

type pushManager struct {
	dir   string
	path  string
	vault *vault.Vault

	mu     sync.Mutex
	cfg    pushConfig
	token  string
	sealed string
	closed bool

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
	return pushConfig{Approval: true, Task: true, Reply: true}
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
	f := pushFile{pushConfig: m.cfg}
	if err := json.Unmarshal(raw, &f); err != nil {
		log.Printf("解析 %s 失败：%v（推送保持关闭）", filepath.Base(m.path), err)
		return
	}
	m.cfg = f.pushConfig
	m.sealed = f.SealedToken
	if f.SealedToken != "" {
		if token, err := m.vault.OpenAPIKey(f.SealedToken); err != nil {
			log.Printf("推送 token 无法解密（主密钥可能已更换），请在设置里重新填写")
		} else {
			m.token = token
		}
	}
}

func (m *pushManager) view() pushView {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.viewLocked()
}

func (m *pushManager) viewLocked() pushView {
	return pushView{
		Enabled:    m.cfg.Enabled,
		URL:        m.cfg.URL,
		Topic:      m.cfg.Topic,
		PublicHost: m.cfg.PublicHost,
		Approval:   m.cfg.Approval,
		Task:       m.cfg.Task,
		Reply:      m.cfg.Reply,
		HasToken:   m.token != "",
	}
}

// update 校验并保存设置。token 为 nil 时保留旧值，空串时清除。
func (m *pushManager) update(d pushDraft) (pushView, error) {
	cfg, err := normalizePushDraft(d)
	if err != nil {
		return pushView{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sealed, token := m.sealed, m.token
	if d.Token != nil {
		plain := strings.TrimSpace(*d.Token)
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
	f := pushFile{pushConfig: cfg, SealedToken: sealed}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return pushView{}, err
	}
	if err := writePrivate(m.dir, m.path, raw); err != nil {
		return pushView{}, err
	}
	m.cfg, m.sealed, m.token = cfg, sealed, token
	return m.viewLocked(), nil
}

func normalizePushDraft(d pushDraft) (pushConfig, error) {
	cfg := pushConfig{
		Enabled:    d.Enabled,
		URL:        strings.TrimSpace(d.URL),
		Topic:      strings.TrimSpace(d.Topic),
		PublicHost: strings.TrimSpace(d.PublicHost),
		Approval:   d.Approval,
		Task:       d.Task,
		Reply:      d.Reply,
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
	cfg, token := m.cfg, m.token
	m.mu.Unlock()
	if cfg.URL == "" || cfg.Topic == "" {
		return errors.New("先填写 ntfy 地址和主题")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return m.sender.Send(ctx, cfg, token, pushNotice{
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
			cfg, token := m.cfg, m.token
			m.mu.Unlock()
			if !cfg.Enabled || !pushKindEnabled(cfg, n.Kind) {
				continue
			}
			ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
			err := m.sender.Send(ctx, cfg, token, n)
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

func (s *ntfySender) Send(ctx context.Context, cfg pushConfig, token string, n pushNotice) error {
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
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
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
	return fmt.Errorf("ntfy 返回 %d：%s", resp.StatusCode, strings.TrimSpace(string(raw)))
}
