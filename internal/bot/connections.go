package bot

import (
	"context"
	"path/filepath"

	"github.com/kiry163/reeve/internal/exec"
	"github.com/kiry163/reeve/internal/exec/cli"
)

// agentConnection 是旧版 agent-connection.json 的形状，也是 GET /api/settings/connections 的响应形状。
// 权威配置在 executors.json；写入口只有 PUT /api/executors 与 /agent。
type agentConnection struct {
	CLI string `json:"cli"` // Empty selects the built-in agent.
	cli.Options
}

type connectionsView struct {
	agentConnection
	CLIs []cli.Status `json:"clis"`
}

func (b *Bot) connectionPath() string { return filepath.Join(b.cfg.DataDir, "agent-connection.json") }

// selectedCLI 返回当前默认的外部执行器；内置执行器返回空串（与旧行为一致）。
func (b *Bot) selectedCLI() string {
	b.modelMu.RLock()
	defer b.modelMu.RUnlock()
	if b.executors.Default == string(exec.KindBuiltin) {
		return ""
	}
	return b.executors.Default
}

// cliOptions 返回指定 CLI 保存的模型与认证设置；没有保存过时用该 CLI 的默认
// （官方订阅、CLI 默认模型；Pi 固定走 reeve 的 API 渠道）。
func (b *Bot) cliOptions(id string) cli.Options {
	b.modelMu.RLock()
	opts, ok := b.executors.CLIs[id]
	b.modelMu.RUnlock()
	if ok {
		return opts
	}
	opts, _ = cli.ValidateOptions(exec.Kind(id), cli.Options{})
	return opts
}

// connectionSettings 只读当前默认执行器与它的选项，不跑任何探测（/api/info 用）。
func (b *Bot) connectionSettings() agentConnection {
	id := b.selectedCLI()
	opts := cli.Options{}
	if id != "" {
		opts = b.cliOptions(id)
	}
	return agentConnection{CLI: id, Options: opts}
}

// connections 返回默认执行器设置和每个 CLI 的安装/版本情况。probe=false 时只刷新
// 安装状态与已缓存的探测结果，不起 `--version` 子进程（安装轮询用）。
func (b *Bot) connections(ctx context.Context, probe bool) connectionsView {
	statuses := cli.DetectCached()
	if probe {
		statuses = cli.Detect(ctx)
	}
	return connectionsView{b.connectionSettings(), statuses}
}
