package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// RegisterBuiltins 把产品内置的 10 条斜线命令注册到 r。
//
// 命令只依赖 BotAPI，不感知聊天层：用户输入的命令直接生效（用户就是授权者），
// 结果由调用方写成 command.result 事件，不进入模型上下文。
func RegisterBuiltins(r *Registry) {
	r.register(Definition{
		Name:    "help",
		Aliases: []string{"h"},
		Summary: "列出命令与用法",
		Usage:   "/help",
		Run: func(context.Context, Input) Result {
			return Result{Text: r.help(), OK: true}
		},
	})
	r.register(Definition{
		Name:    "stop",
		Summary: "停止所有任务与当前回复",
		Usage:   "/stop",
		Run: func(_ context.Context, in Input) Result {
			n := in.Bot.StopAll()
			if n == 0 {
				return Result{Text: "没有正在运行或排队的任务。", OK: true}
			}
			return Result{Text: fmt.Sprintf("已停止 %d 个任务。", n), OK: true}
		},
	})
	r.register(Definition{
		Name:    "clear",
		Summary: "清空当前会话上下文",
		Usage:   "/clear",
		Run: func(_ context.Context, in Input) Result {
			if err := in.Bot.ClearContext(); err != nil {
				return Result{Text: "清空上下文失败：" + err.Error(), OK: false}
			}
			return Result{Text: "已清空当前会话上下文（旧消息已隐藏，历史文件仍保留）。", OK: true}
		},
	})
	r.register(Definition{
		Name:    "model",
		Summary: "查看或切换聊天模型",
		Usage:   "/model [name]",
		Run: func(_ context.Context, in Input) Result {
			if len(in.Args) > 1 {
				return Result{Text: "用法：/model [name]", OK: false}
			}
			name := ""
			if len(in.Args) == 1 {
				name = in.Args[0]
			}
			if name == "" {
				cur, err := in.Bot.SwitchModel("")
				if err != nil {
					return Result{Text: "读取当前模型失败：" + err.Error(), OK: false}
				}
				return Result{Text: "当前聊天模型：" + cur, OK: true}
			}
			text, err := in.Bot.SwitchModel(name)
			if err != nil {
				return Result{Text: "切换模型失败：" + err.Error(), OK: false}
			}
			return Result{Text: text, OK: true}
		},
	})
	r.register(Definition{
		Name:    "agent",
		Aliases: []string{"ex"},
		Summary: "查看/切换默认执行器",
		Usage:   "/agent [list|<kind>]",
		Run: func(_ context.Context, in Input) Result {
			if len(in.Args) > 1 {
				return Result{Text: "用法：/agent [list|<kind>]", OK: false}
			}
			allowed := strings.Join(in.Bot.AllowedExecutors(), "、")
			if len(in.Args) == 0 {
				return Result{Text: fmt.Sprintf("默认执行器：%s\n白名单：%s", in.Bot.DefaultExecutor(), allowed), OK: true}
			}
			if in.Args[0] == "list" {
				return Result{Text: "白名单：" + allowed, OK: true}
			}
			text, err := in.Bot.SwitchExecutor(in.Args[0])
			if err != nil {
				return Result{Text: "切换执行器失败：" + err.Error(), OK: false}
			}
			return Result{Text: text, OK: true}
		},
	})
	r.register(Definition{
		Name:    "env",
		Aliases: []string{"e"},
		Summary: "查看或修改环境变量",
		Usage:   "/env [list|set K=V|unset K]",
		Run:     runEnv,
	})
	r.register(Definition{
		Name:    "skills",
		Summary: "列出可用技能",
		Usage:   "/skills",
		Run: func(_ context.Context, in Input) Result {
			names := in.Bot.ListSkills()
			if len(names) == 0 {
				return Result{Text: "还没有加载任何技能。", OK: true}
			}
			sort.Strings(names)
			return Result{Text: "可用技能：\n- " + strings.Join(names, "\n- "), OK: true}
		},
	})
	r.register(Definition{
		Name:    "context",
		Summary: "查看上下文用量（消息数、估算 token、压缩阈值）",
		Usage:   "/context",
		Run: func(_ context.Context, in Input) Result {
			return Result{Text: in.Bot.ContextInfo(), OK: true}
		},
	})
	r.register(Definition{
		Name:    "tasks",
		Summary: "查看任务摘要（运行/排队/暂停/完成）",
		Usage:   "/tasks",
		Run: func(_ context.Context, in Input) Result {
			return Result{Text: in.Bot.ListTasks(), OK: true}
		},
	})
	r.register(Definition{
		Name:    "approve",
		Summary: "回答一个待审批的操作",
		Usage:   "/approve <id> allow|deny",
		Run:     runApprove,
	})
}

const envUsage = "用法：/env [list|set K=V|unset K]\n例如：/env set HTTPS_PROXY=http://127.0.0.1:7890（实例级，所有 Bot 共享）"

// runEnv 解析 /env 的子命令；所有操作都作用于实例级环境变量。
func runEnv(_ context.Context, in Input) Result {
	args := in.Args
	if len(args) == 0 || (len(args) == 1 && args[0] == "list") {
		return listEnv(in)
	}
	switch args[0] {
	case "list":
		if len(args) > 1 {
			return Result{Text: envUsage, OK: false}
		}
		return listEnv(in)
	case "set":
		rest := args[1:]
		if len(rest) == 0 {
			return Result{Text: envUsage, OK: false}
		}
		name, value := "", ""
		if k, v, ok := strings.Cut(rest[0], "="); ok {
			if len(rest) > 1 {
				return Result{Text: envUsage, OK: false}
			}
			name, value = k, v
		} else {
			if len(rest) < 2 {
				return Result{Text: envUsage, OK: false}
			}
			name, value = rest[0], strings.Join(rest[1:], " ")
		}
		if err := in.Bot.SetEnv(name, value); err != nil {
			return Result{Text: "设置环境变量失败：" + err.Error(), OK: false}
		}
		return Result{Text: fmt.Sprintf("已设置 %s（实例级，所有 Bot 共享）。", name), OK: true}
	case "unset":
		rest := args[1:]
		if len(rest) != 1 || strings.Contains(rest[0], "=") {
			return Result{Text: envUsage, OK: false}
		}
		vars, err := in.Bot.ListEnv()
		if err != nil {
			return Result{Text: "读取环境变量失败：" + err.Error(), OK: false}
		}
		if _, ok := vars[rest[0]]; !ok {
			return Result{Text: fmt.Sprintf("实例级没有 %s。", rest[0]), OK: false}
		}
		if err := in.Bot.UnsetEnv(rest[0]); err != nil {
			return Result{Text: "删除环境变量失败：" + err.Error(), OK: false}
		}
		return Result{Text: fmt.Sprintf("已删除 %s（实例级）。", rest[0]), OK: true}
	default:
		return Result{Text: envUsage, OK: false}
	}
}

func listEnv(in Input) Result {
	vars, err := in.Bot.ListEnv()
	if err != nil {
		return Result{Text: "读取环境变量失败：" + err.Error(), OK: false}
	}
	if len(vars) == 0 {
		return Result{Text: "还没有设置环境变量。用 /env set K=V 添加。", OK: true}
	}
	var b strings.Builder
	writeEnvLayer(&b, "实例级（所有 Bot 共享）", vars)
	// 命令结果会被写成 command.result 事件并进入 SSE 重放，值不能落进事件日志；
	// 只列变量名与来源，值去设置页看。
	text := strings.TrimRight(b.String(), "\n") + "\n值请在设置的「环境变量」里查看。"
	return Result{Text: text, OK: true}
}

// writeEnvLayer 只输出变量名与固定占位符，绝不输出值。
func writeEnvLayer(b *strings.Builder, label string, vars map[string]string) {
	if len(vars) == 0 {
		return
	}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(b, "%s：\n", label)
	for _, k := range keys {
		fmt.Fprintf(b, "  %s  <已隐藏>\n", k)
	}
}

// runApprove 把命令行决定交给 BotAPI，与卡片按钮等价（仍绑定 args_hash）。
func runApprove(_ context.Context, in Input) Result {
	if len(in.Args) != 2 {
		return Result{Text: "用法：/approve <id> allow|deny", OK: false}
	}
	id, decision := in.Args[0], strings.ToLower(in.Args[1])
	var label string
	switch decision {
	case "allow", "once":
		label = "已批准"
	case "session":
		label = "已批准（本任务内有效）"
	case "deny":
		label = "已拒绝"
	default:
		return Result{Text: "决定必须是 allow 或 deny。用法：/approve <id> allow|deny", OK: false}
	}
	if err := in.Bot.Approve(id, decision); err != nil {
		return Result{Text: "审批失败：" + err.Error(), OK: false}
	}
	return Result{Text: fmt.Sprintf("审批 %s %s。", id, label), OK: true}
}
