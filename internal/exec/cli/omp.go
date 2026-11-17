package cli

// 实测记录（omp 18.11.0，2026-11-15，macOS arm64）：
//
//	$ omp --version
//	omp/18.11.0
//
//	$ omp --mode=json -p "Reply with the single word: pong" --no-tools --no-session --no-skills --no-rules
//	{"type":"session","version":3,"id":"01a1df50-…","cwd":"/tmp/omptest"}
//	{"type":"agent_start"}
//	{"type":"turn_start"}
//	{"type":"message_start","message":{"role":"user",…}}
//	{"type":"message_end","message":{"role":"user",…}}
//	{"type":"message_start","message":{"role":"assistant","content":[{"type":"text","text":"pong"}],…}}
//	{"type":"message_update","assistantMessageEvent":{"type":"text_start","contentIndex":0}}
//	{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"p"}}
//	{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"ong"}}
//	{"type":"message_update","assistantMessageEvent":{"type":"text_end","contentIndex":0,"content":"pong"}}
//	{"type":"turn_end","message":{"role":"assistant",…},"toolResults":[]}
//	{"type":"agent_end","messages":[…],"isTerminal":true,"yielded":true}
//
// 工具调用（同一版本，允许 bash 工具；`omp --mode=json -p "Call the bash tool …"`）：
//
//	{"type":"message_update","assistantMessageEvent":{"type":"toolcall_start","contentIndex":1}}
//	{"type":"message_update","assistantMessageEvent":{"type":"toolcall_delta","contentIndex":1,"delta":"{\""}}
//	…（62 条 delta，拼起来就是参数 JSON）
//	{"type":"message_update","assistantMessageEvent":{"type":"toolcall_end","contentIndex":1,"toolCall":{"id":"call_00_…","name":"bash","arguments":{…},"partialArgs":"{…}","streamIndex":0}}}
//	{"type":"tool_execution_start","toolCallId":"call_00_…","toolName":"bash","args":{…},"intent":"…"}
//	{"type":"tool_execution_update","toolCallId":"call_00_…","toolName":"bash","args":{…},"partialResult":{"content":[{"type":"text","text":"omp-tool-ok\n"}],"details":{}}}
//	{"type":"tool_execution_end","toolCallId":"call_00_…","toolName":"bash","result":{"content":[{"type":"text","text":"omp-tool-ok\n"}],"details":{…}},"isError":false}
//
// 注意实测与计划里的样例行有两处差别：toolcall_start 上没有 name（名字在 toolcall_end.toolCall.name），
// tool_execution_end.result 是对象（content[].text），不是纯字符串。两种形状解析器都要吃。
//
// 启动参数：-p 非交互、--mode=json 事件流、--no-session 不落会话、--model 指定模型、
// --max-time 限制总时长。提示词走位置参数（argv），因此有长度上限（见 maxArgvPrompt）。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/kiry163/reeve/internal/exec"
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

type ompRunner struct{ look Lookup }

func NewOMP() exec.Runner { return &ompRunner{look: defaultLookup} }

func (r *ompRunner) Kind() exec.Kind { return exec.KindOMP }

func (r *ompRunner) Available(ctx context.Context) error {
	exe, prefix, err := r.look(exec.KindOMP)
	if err != nil {
		return errors.New("找不到 omp，请先安装并放进 PATH")
	}
	return probeAvailable(ctx, exe, prefix)
}

func (r *ompRunner) Start(ctx context.Context, req exec.Request, sink exec.Sink) (exec.Handle, error) {
	exe, prefix, err := r.look(exec.KindOMP)
	if err != nil {
		return nil, errors.New("找不到 omp，请先安装并放进 PATH")
	}
	prompt := buildPrompt(req)
	if len(prompt) > maxArgvPrompt {
		return nil, fmt.Errorf("omp 的任务说明过长（%d 字节，上限 %d），请缩短后重试", len(prompt), maxArgvPrompt)
	}
	args := []string{"-p", "--mode=json", "--no-session", "--no-skills", "--no-rules"}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.MaxDuration > 0 {
		args = append(args, "--max-time", strconv.Itoa(int(req.MaxDuration.Seconds())))
	}
	args = append(args, prompt)
	st := newOMPState()
	return Launch(ctx, Spec{
		Kind:      exec.KindOMP,
		Exe:       exe,
		Args:      append(prefix, args...),
		Dir:       req.Workspace,
		Env:       req.Env,
		MaxOutput: maxCLIOutput,
		Parse: func(line []byte, sink exec.Sink, fail func(string)) {
			parseOMP(line, st, sink, fail)
		},
	}, sink)
}

type ompToolCall struct {
	id    string
	name  string
	input strings.Builder
}

// ompToolPayload 是 toolcall_end 上的 toolCall 字段（实测：名字与参数都在这里）。
type ompToolPayload struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Arguments   json.RawMessage `json:"arguments"`
	PartialArgs string          `json:"partialArgs"`
}

// ompState 只需要跨行记住“正在流式输出的那个工具调用的名字与参数”：
// 参数是 62 条 delta 拼出来的，单行解析拿不到完整调用。
type ompState struct {
	tools map[int]*ompToolCall
	shown map[string]bool // 已经发过 EventTool 的 toolCallId（tool_execution_start 别重复发）
}

func newOMPState() *ompState {
	return &ompState{tools: map[int]*ompToolCall{}, shown: map[string]bool{}}
}

func parseOMP(line []byte, st *ompState, sink exec.Sink, fail func(string)) {
	line = bytes.TrimPrefix(line, utf8BOM)
	var ev struct {
		Type                  string `json:"type"`
		AssistantMessageEvent struct {
			Type         string          `json:"type"`
			ContentIndex int             `json:"contentIndex"`
			Delta        string          `json:"delta"`
			Name         string          `json:"name"`
			ToolCall     *ompToolPayload `json:"toolCall"`
		} `json:"assistantMessageEvent"`
		ToolCallID string          `json:"toolCallId"`
		ToolName   string          `json:"toolName"`
		Args       json.RawMessage `json:"args"`
		IsError    bool            `json:"isError"`
		Result     json.RawMessage `json:"result"`
		Error      json.RawMessage `json:"error"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	switch ev.Type {
	case "message_update":
		a := ev.AssistantMessageEvent
		switch a.Type {
		case "text_delta":
			if a.Delta != "" {
				sink.Emit(exec.Event{Kind: exec.EventText, Text: a.Delta})
			}
		case "toolcall_start":
			call := st.tools[a.ContentIndex]
			if call == nil {
				call = &ompToolCall{}
				st.tools[a.ContentIndex] = call
			}
			fillOMPTool(call, a.Name, a.ToolCall)
		case "toolcall_delta":
			if call := st.tools[a.ContentIndex]; call != nil {
				call.input.WriteString(a.Delta)
			}
		case "toolcall_end":
			call := st.tools[a.ContentIndex]
			delete(st.tools, a.ContentIndex)
			if call == nil {
				call = &ompToolCall{}
			}
			fillOMPTool(call, a.Name, a.ToolCall)
			if call.name == "" {
				return // 名字在 tool_execution_start 里才给，那里再补
			}
			if call.input.Len() == 0 {
				// 没有 delta 流（或已经丢掉）时用 toolcall_end 自带的参数。
				if a.ToolCall != nil {
					if a.ToolCall.PartialArgs != "" {
						call.input.WriteString(a.ToolCall.PartialArgs)
					} else if len(a.ToolCall.Arguments) > 0 {
						call.input.Write(a.ToolCall.Arguments)
					}
				}
			}
			sink.Emit(exec.Event{Kind: exec.EventTool, Tool: call.name, Input: call.input.String()})
			if call.id != "" {
				st.shown[call.id] = true
			}
		}
	case "tool_execution_start":
		// 正常路径已经在 toolcall_end 发过 EventTool；只有 toolcall_end 没带名字时才在这里兜底。
		if ev.ToolCallID != "" && st.shown[ev.ToolCallID] {
			return
		}
		if ev.ToolName == "" {
			return
		}
		sink.Emit(exec.Event{Kind: exec.EventTool, Tool: ev.ToolName, Input: strings.TrimSpace(string(ev.Args))})
		if ev.ToolCallID != "" {
			st.shown[ev.ToolCallID] = true
		}
	case "tool_execution_end":
		out, errText := ompToolResult(ev.Result, ev.IsError)
		sink.Emit(exec.Event{Kind: exec.EventToolResult, Tool: ev.ToolName, Output: out, Err: errText})
	case "error":
		fail(ompErrorText(ev.Error))
	}
}

func fillOMPTool(call *ompToolCall, name string, toolCall *ompToolPayload) {
	if name != "" {
		call.name = name
	}
	if toolCall == nil {
		return
	}
	if toolCall.Name != "" {
		call.name = toolCall.Name
	}
	if toolCall.ID != "" {
		call.id = toolCall.ID
	}
}

func ompToolResult(raw json.RawMessage, isErr bool) (string, string) {
	text := ompResultText(raw)
	if !isErr {
		return truncate(text, resultTextLimit), ""
	}
	if text == "" {
		text = "工具执行失败"
	}
	return "", truncate(text, errorDetailLimit)
}

// ompResultText 兼容两种 result：字符串（计划里的样例行）和
// 对象 {"content":[{"type":"text","text":"…"}],"details":{…}}（实测形状）。
func ompResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var obj struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &obj) == nil && len(obj.Content) > 0 {
		var b strings.Builder
		for _, c := range obj.Content {
			b.WriteString(c.Text)
		}
		if text := strings.TrimSpace(b.String()); text != "" {
			return text
		}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(string(raw))
}

func ompErrorText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s
	}
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &m) == nil && m.Message != "" {
		return m.Message
	}
	return "omp 报错"
}
