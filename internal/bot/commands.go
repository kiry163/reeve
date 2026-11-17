package bot

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kiry163/reeve/internal/builtin"
	"github.com/kiry163/reeve/internal/commands"
)

// Bot 是斜线命令能调用的全部能力。
var _ commands.BotAPI = (*Bot)(nil)

// 命令行的长度上限：command.result 会进事件日志并被 SSE 重放，不能让一行
// 近 1 MiB 的文本落盘；事件里的命令名再单独截断到 32 字节。
const (
	maxCommandLine = 4 << 10
	maxCommandName = 32
)

// CommandLine 执行一行斜线命令。返回的第二个值表示这行文本是不是命令：
// 不是命令时调用方按普通消息处理；是命令时结果已经写成 command.result 事件，
// 不进入模型上下文。
func (b *Bot) CommandLine(ctx context.Context, text string) (commands.Result, bool) {
	if b.commands == nil {
		return commands.Result{}, false
	}
	line := strings.TrimSpace(text)
	if strings.HasPrefix(line, "/") && len(line) > maxCommandLine {
		res := commands.Result{Text: fmt.Sprintf("命令太长（%d 字节）：一行命令最多 %d 字节，拆短一点再试。", len(line), maxCommandLine), OK: false}
		b.store.Append("command.result", map[string]any{
			"command": commandName(line),
			"text":    res.Text,
			"ok":      res.OK,
		}, false)
		return res, true
	}
	res, ok := b.commands.Execute(ctx, b, text)
	if !ok {
		return commands.Result{}, false
	}
	b.store.Append("command.result", map[string]any{
		"command": commandName(text),
		"text":    res.Text,
		"ok":      res.OK,
	}, false)
	return res, true
}

// commandName 取一行命令的命令名（含开头的 /），用于 command.result 事件。
// 名字截断到 32 字节（按 rune 边界），避免异常输入把长字符串写进事件。
func commandName(line string) string {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 0 {
		return "/"
	}
	name := fields[0]
	if len(name) <= maxCommandName {
		return name
	}
	cut := 0
	for i := range name {
		if i > maxCommandName {
			break
		}
		cut = i
	}
	return name[:cut]
}

// StopAll 停止所有任务与正在进行的回复，但不往聊天里写回复（命令结果由 command.result 事件承载）。
func (b *Bot) StopAll() int { return b.stopAll() }

// ClearContext 清空当前会话上下文：旧消息标 hidden（历史文件保留），开新 generation
// 让进行中的回合失效。
func (b *Bot) ClearContext() error {
	b.turnMu.Lock()
	b.turnGeneration++
	if b.turnCancel != nil {
		b.turnCancel(errStopped)
	}
	// 已处理消息 + 当前回合正在处理的输入都要作废；真正排队、尚未开始的输入保持未处理。
	ids := b.store.HandledIDs()
	ids = append(ids, b.activeTurnIDs...)
	b.turnMu.Unlock()
	b.store.Hide(ids)
	b.store.DismissPendingCards()
	return nil
}

// ListTasks 返回任务摘要，供 /tasks 使用。
func (b *Bot) ListTasks() string { return b.listTasks("all", 10) }

// ContextInfo 返回一行上下文用量：消息数、估算 token、压缩阈值与工具调用次数。
func (b *Bot) ContextInfo() string {
	msgs := b.store.History(10000)
	history := make([]builtin.Message, 0, len(msgs))
	tools := 0
	for _, m := range msgs {
		role, text := m.Role, msgText(m)
		if role == "notice" {
			role, text = "user", "[系统通知] "+noticeText(m)
		}
		if text != "" {
			history = append(history, builtin.Message{Role: role, Content: text})
		}
		tools += len(m.Tools)
	}
	return fmt.Sprintf("上下文：%d 条消息，估算 %d token（压缩阈值 %d），工具调用 %d 次",
		len(msgs), builtin.EstimateTokens(history), b.cfg.ContextTokens, tools)
}

// SwitchModel 查看或切换聊天模型。没有参数时返回当前模型名；有参数时要求模型存在于
// 已启用渠道的模型列表里，写回本 Bot 的模型选择。
func (b *Bot) SwitchModel(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		_, current := b.resolveModel(true)
		if current == "" {
			return "", errors.New("还没有配置聊天模型")
		}
		return current, nil
	}
	channels := b.pool.public()
	cur := b.GetSettings()
	chosen := ""
	for _, c := range channels {
		if !c.Enabled || !slices.Contains(c.Models, name) || slices.Contains(c.HiddenModels, name) {
			continue
		}
		if chosen == "" {
			chosen = c.ID
		}
		if c.ID == cur.DefaultProviderID {
			chosen = c.ID
			break
		}
	}
	if chosen == "" {
		return "", fmt.Errorf("已配置的渠道里没有模型 %s", name)
	}
	draft := APISettings{
		DefaultProviderID: chosen,
		Model:             name,
		AgentProviderID:   cur.AgentProviderID,
		AgentModel:        cur.AgentModel,
	}
	if _, err := b.saveRoutes(draft); err != nil {
		return "", err
	}
	return "已切换到 " + name, nil
}

// SwitchExecutor 查看或切换默认执行器。只有白名单内的执行器能成为默认值；
// 白名单本身由用户在设置页修改，模型没有这个能力。
func (b *Bot) SwitchExecutor(kind string) (string, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		return b.DefaultExecutor(), nil
	}
	if err := b.updateExecutors(func(ex *Executors) error {
		if !slices.Contains(ex.Allowed, kind) {
			return fmt.Errorf("%s 不在白名单里，当前可用：%s；先用设置页把它加入白名单", kind, strings.Join(ex.Allowed, "、"))
		}
		ex.Default = kind
		return nil
	}); err != nil {
		return "", err
	}
	return "已切换到 " + kind, nil
}

// Approve 把 /approve 的命令行决定交给 Decide；allow/once 等价于只批准这一次。
func (b *Bot) Approve(id, decision string) error {
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "allow", "once":
		decision = "once"
	case "session":
		decision = "session"
	case "deny":
		decision = "deny"
	default:
		return fmt.Errorf("未知的决定: %s", decision)
	}
	return b.Decide(id, decision)
}
