package cli

// 实测记录（codex-cli 0.169.0，2026-11-15，macOS arm64，本机上游网关未启动）：
//
//	$ codex --version
//	codex-cli 0.169.0
//
//	$ codex exec --json --skip-git-repo-check --sandbox read-only "Reply with the single word pong"
//	{"type":"thread.started","thread_id":"01a1df48-6bc6-7bb2-8999-c87233b8ac41"}
//	{"type":"item.completed","item":{"id":"item_0","type":"error","message":"Codex is ignoring 1 unrecognized configuration setting. …"}}
//	{"type":"turn.started"}
//	{"type":"error","message":"Reconnecting... 1/5 (unexpected status 502 Bad Gateway: …)"}
//	{"type":"turn.failed","error":{"message":"unexpected status 502 Bad Gateway: Unknown error, url: http://127.0.0.1:8100/responses"}}
//
// 结论：
//   - turn.failed.error.message 是失败原因，只有它代表终态失败；
//   - {"type":"error",…} 是重连提示，重连成功后不该把任务判死，所以解析器忽略它；
//   - item.completed 且 item.type=="error" 是解释性告警（配置项被忽略），不算失败。
//   - agent_message 行本机没有观测到（网关不可用），字段取自 codex --json 的文档化契约与旧 builtin/cli.go 的实现：
//     {"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"pong"}}
//
// 启动参数：exec 非交互、--json JSONL 事件流、--sandbox workspace-write、"-" 表示提示词从 stdin 读
// （提示词不经过 argv，因此没有长度上限问题）。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kiry163/reeve/internal/exec"
)

type codexRunner struct{ look Lookup }

func NewCodex() exec.Runner { return &codexRunner{look: defaultLookup} }

func (r *codexRunner) Kind() exec.Kind { return exec.KindCodex }

func (r *codexRunner) Available(ctx context.Context) error {
	exe, prefix, err := r.look(exec.KindCodex)
	if err != nil {
		return errors.New("找不到 codex，请先安装并放进 PATH")
	}
	return probeAvailable(ctx, exe, prefix)
}

func (r *codexRunner) Start(ctx context.Context, req exec.Request, sink exec.Sink) (exec.Handle, error) {
	exe, prefix, err := r.look(exec.KindCodex)
	if err != nil {
		return nil, errors.New("找不到 codex，请先安装并放进 PATH")
	}
	args := []string{"exec", "--skip-git-repo-check", "--sandbox", "workspace-write", "--json"}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	args = append(args, "-")
	// codex 一次运行可能给出多条 agent_message；旧路径只保留最后一条，这里用 FinalText 保持一致。
	var lastText, lastError string
	parse := func(line []byte, s exec.Sink, fail func(string)) {
		parseCodex(line, sinkFunc(func(e exec.Event) {
			if e.Kind == exec.EventText {
				lastText = e.Text
			}
			s.Emit(e)
		}), fail, func(msg string) { lastError = msg })
	}
	return Launch(ctx, Spec{
		Kind:       exec.KindCodex,
		Exe:        exe,
		Args:       append(prefix, args...),
		Stdin:      buildPrompt(req),
		Dir:        req.Workspace,
		Env:        req.Env,
		MaxOutput:  maxCLIOutput,
		Parse:      parse,
		FinalText:  func() string { return lastText },
		Diagnostic: func() string { return lastError },
	}, sink)
}

// parseCodex 解析 codex exec --json 的一行。fail 是终态失败（turn.failed），
// diag 只记录重连提示之类的诊断信息，不改变是否失败的判定。
func parseCodex(line []byte, sink exec.Sink, fail, diag func(string)) {
	line = bytes.TrimPrefix(line, utf8BOM)
	var ev struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Item    struct {
			Type             string `json:"type"`
			Text             string `json:"text"`
			Command          string `json:"command"`
			AggregatedOutput string `json:"aggregated_output"`
			ExitCode         *int   `json:"exit_code"`
		} `json:"item"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	switch ev.Type {
	case "item.completed":
		switch ev.Item.Type {
		case "agent_message":
			if text := strings.TrimSpace(ev.Item.Text); text != "" {
				sink.Emit(exec.Event{Kind: exec.EventText, Text: text})
			}
		case "command_execution":
			errText := ""
			if ev.Item.ExitCode != nil && *ev.Item.ExitCode != 0 {
				errText = fmt.Sprintf("命令退出码 %d", *ev.Item.ExitCode)
			}
			sink.Emit(exec.Event{Kind: exec.EventToolResult, Tool: "command", Input: ev.Item.Command,
				Output: truncate(strings.TrimSpace(ev.Item.AggregatedOutput), resultTextLimit), Err: errText})
		}
	case "turn.failed":
		msg := strings.TrimSpace(ev.Error.Message)
		if msg == "" {
			msg = "codex 任务未完成"
		}
		fail(msg)
	case "error":
		// 重连提示不是终态：记下来当诊断（进程非零退出且没有 stderr 时用），不判失败。
		if msg := strings.TrimSpace(ev.Message); msg != "" {
			diag(msg)
		}
	}
}
