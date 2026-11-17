package cli

// 实测记录：本机未安装 pi（2026-11-15，macOS arm64，`command -v pi` 为空）：
//
//	$ pi --version
//	(error: command not found: pi)
//
// 所以 `--version` 的正常输出未观测到；探测按通用契约解析输出里的版本号，
// 解析不出时提示“可能装坏了”。执行契约来自既有 `--print --no-session` 用法：
//
//	$ pi --print --no-session               # 提示词走 stdin，输出纯文本（非 JSON）
//	$ pi --print --no-session --provider reeve --model <model>
//
// 因此解析器只做一件事：把 stdout 按行收进缓冲，进程退出后一次性发一条 EventText
// （与旧路径“一段式输出”一致；不把中间行当成独立回复）。
// 渠道模式（--provider reeve）由 ChannelEnv 提供临时配置目录与网关令牌。

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kiry163/reeve/internal/exec"
)

// piProvider 是写进 Pi 临时配置里的提供方名字。
const piProvider = "reeve"

type piRunner struct{ look Lookup }

func NewPi() exec.Runner { return &piRunner{look: defaultLookup} }

func (r *piRunner) Kind() exec.Kind { return exec.KindPi }

func (r *piRunner) Available(ctx context.Context) error {
	exe, prefix, err := r.look(exec.KindPi)
	if err != nil {
		return errors.New("找不到 pi，请先安装并放进 PATH")
	}
	return probeAvailable(ctx, exe, prefix)
}

func (r *piRunner) Start(ctx context.Context, req exec.Request, sink exec.Sink) (exec.Handle, error) {
	exe, prefix, err := r.look(exec.KindPi)
	if err != nil {
		return nil, errors.New("找不到 pi，请先安装并放进 PATH")
	}
	args := []string{"--print", "--no-session"}
	if req.Model != "" {
		args = append(args, "--provider", piProvider, "--model", req.Model)
	}
	parser := &piParser{}
	return Launch(ctx, Spec{
		Kind:      exec.KindPi,
		Exe:       exe,
		Args:      append(prefix, args...),
		Stdin:     buildPrompt(req),
		Dir:       req.Workspace,
		Env:       req.Env,
		MaxOutput: maxCLIOutput,
		Parse: func(line []byte, _ exec.Sink, _ func(string)) {
			parser.add(line)
		},
		Done: parser.done,
	}, sink)
}

// piParser 缓冲整段纯文本输出；done 在进程退出、管道读完之后发一条 EventText。
type piParser struct {
	lines []string
	size  int
}

func (p *piParser) add(line []byte) {
	if p.size >= maxCLIOutput {
		return
	}
	text := string(line)
	if room := maxCLIOutput - p.size; len(text) > room {
		text = text[:room]
	}
	p.lines = append(p.lines, text)
	p.size += len(text) + 1
}

func (p *piParser) done(sink exec.Sink) {
	text := strings.TrimSpace(strings.Join(p.lines, "\n"))
	if text == "" {
		return
	}
	sink.Emit(exec.Event{Kind: exec.EventText, Text: text})
}

// Gateway 是 reeve 的本机模型网关：给一次 CLI 运行发临时令牌，令牌只能用设置里选的那个
// API 渠道和模型，真实 Key 留在 reeve 里。bot 启动时用 SetGateway 注入。
type Gateway interface {
	Issue(channel, model string, ttl time.Duration) (baseURL, token string, revoke func(), err error)
}

var gateway struct {
	sync.RWMutex
	g Gateway
}

// SetGateway 由 Bots 启动时设置。
func SetGateway(g Gateway) {
	gateway.Lock()
	gateway.g = g
	gateway.Unlock()
}

func currentGateway() Gateway {
	gateway.RLock()
	defer gateway.RUnlock()
	return gateway.g
}

// ChannelEnv 给这一次 Pi 运行准备临时配置目录（PI_CODING_AGENT_DIR）：models.json 里只有一个
// 指向本机网关的提供方，Key 写成 $REEVE_GATEWAY_TOKEN，令牌放在环境变量里。
// base 是已经按认证方式过滤过的环境（通常来自 CommandEnv）；返回的环境去掉了调用方原有的
// PI_CODING_AGENT_DIR / REEVE_GATEWAY_TOKEN。运行结束必须调用 cleanup：作废令牌并删目录。
func ChannelEnv(base []string, opts Options) (env []string, cleanup func(), err error) {
	if opts.Channel == "" || opts.Model == "" {
		return nil, nil, errors.New("还没有给 Pi 选择 API 渠道和模型：请在「设置 → 模型与 Agent」里选择")
	}
	gw := currentGateway()
	if gw == nil {
		return nil, nil, errors.New("reeve 的本机模型网关没有启动")
	}
	baseURL, token, revoke, err := gw.Issue(opts.Channel, opts.Model, 6*time.Hour)
	if err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp("", "reeve-pi-")
	if err != nil {
		revoke()
		return nil, nil, err
	}
	cleanup = func() { revoke(); _ = os.RemoveAll(dir) }
	cfg := map[string]any{"providers": map[string]any{piProvider: map[string]any{
		"baseUrl": baseURL,
		"api":     "openai-completions",
		"apiKey":  "$REEVE_GATEWAY_TOKEN",
		// 第三方中转站常常不认 developer 角色和 reasoning_effort，用最通用的写法
		"compat": map[string]bool{"supportsDeveloperRole": false, "supportsReasoningEffort": false},
		"models": []map[string]any{{"id": opts.Model, "name": opts.Model}},
	}}}
	raw, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "models.json"), raw, 0o600); err != nil {
		cleanup()
		return nil, nil, err
	}
	env = append(withoutEnv(base, "PI_CODING_AGENT_DIR", "REEVE_GATEWAY_TOKEN"),
		"PI_CODING_AGENT_DIR="+dir, "REEVE_GATEWAY_TOKEN="+token)
	return env, cleanup, nil
}
