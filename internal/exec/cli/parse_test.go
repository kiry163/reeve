package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/kiry163/reeve/internal/exec"
)

// 行样本取自上机实测（omp v18.11.0，2026-11-15）；改动解析器必须还能解析它们。
func ompLines(t *testing.T, lines []string) []exec.Event {
	t.Helper()
	var got []exec.Event
	sink := sinkFunc(func(e exec.Event) { got = append(got, e) })
	st := newOMPState()
	fail := func(msg string) { t.Fatalf("不应报错：%s", msg) }
	for _, line := range lines {
		parseOMP([]byte(line), st, sink, fail)
	}
	return got
}

func TestOMPParseTextDelta(t *testing.T) {
	got := ompLines(t, []string{
		`{"type":"session","version":3,"id":"x"}`,
		`{"type":"agent_start"}`,
		`{"type":"message_start","message":{"role":"assistant","content":[{"type":"text","text":"pong"}]}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_start","contentIndex":0}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"p"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"ong"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_end","contentIndex":0,"content":"pong"}}`,
		`{"type":"agent_end","isTerminal":true}`,
	})
	want := []exec.Event{{Kind: exec.EventText, Text: "p"}, {Kind: exec.EventText, Text: "ong"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text 事件不符：%#v", got)
	}
}

func TestOMPParseToolEvents(t *testing.T) {
	got := ompLines(t, []string{
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_start","contentIndex":1,"name":"bash"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_delta","contentIndex":1,"delta":"{\"command\":\"echo omp-tool-ok\"}"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_end","contentIndex":1}}`,
		`{"type":"tool_execution_end","toolName":"bash","isError":false,"result":"omp-tool-ok"}`,
	})
	if len(got) != 2 {
		t.Fatalf("一次工具调用应只发 2 条事件，得到 %#v", got)
	}
	if got[0].Kind != exec.EventTool || got[0].Tool != "bash" || got[0].Input != `{"command":"echo omp-tool-ok"}` {
		t.Fatalf("工具调用事件不符：%#v", got[0])
	}
	if got[1].Kind != exec.EventToolResult || got[1].Output != "omp-tool-ok" || got[1].Err != "" {
		t.Fatalf("工具结果事件不符：%#v", got[1])
	}
}

// 实测形状：toolcall_start 没有 name，名字在 toolcall_end.toolCall 里；result 是对象。
func TestOMPParseRealToolStream(t *testing.T) {
	got := ompLines(t, []string{
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_start","contentIndex":1}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_delta","contentIndex":1,"delta":"{\"command\":"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_delta","contentIndex":1,"delta":"\"echo omp-tool-ok\"}"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_end","contentIndex":1,"toolCall":{"type":"toolCall","id":"call_00_x","name":"bash","arguments":{"command":"echo omp-tool-ok"},"partialArgs":"{\"command\": \"echo omp-tool-ok\"}","streamIndex":0}}}`,
		`{"type":"tool_execution_start","toolCallId":"call_00_x","toolName":"bash","args":{"command":"echo omp-tool-ok"},"intent":"…"}`,
		`{"type":"tool_execution_end","toolCallId":"call_00_x","toolName":"bash","result":{"content":[{"type":"text","text":"omp-tool-ok\n"}],"details":{}},"isError":false}`,
	})
	if len(got) != 2 {
		t.Fatalf("应只发 1 次工具调用 + 1 次结果，得到 %#v", got)
	}
	if got[0].Kind != exec.EventTool || got[0].Tool != "bash" || got[0].Input != `{"command":"echo omp-tool-ok"}` {
		t.Fatalf("工具调用事件不符：%#v", got[0])
	}
	if got[1].Kind != exec.EventToolResult || got[1].Output != "omp-tool-ok" || got[1].Err != "" {
		t.Fatalf("对象形式的 result 未解析成文本：%#v", got[1])
	}
}

func TestOMPParseToolFailure(t *testing.T) {
	got := ompLines(t, []string{
		`{"type":"tool_execution_end","toolName":"bash","isError":true,"result":{"content":[{"type":"text","text":"exit status 1"}]}}`,
	})
	if len(got) != 1 || got[0].Kind != exec.EventToolResult || got[0].Err != "exit status 1" || got[0].Output != "" {
		t.Fatalf("工具失败事件不符：%#v", got)
	}
}

func TestOMPParseErrorEvent(t *testing.T) {
	var errText string
	st := newOMPState()
	parseOMP([]byte(`{"type":"error","error":{"message":"model not found"}}`), st,
		sinkFunc(func(exec.Event) {}), func(msg string) { errText = msg })
	if errText != "model not found" {
		t.Fatalf("error 事件未置错：%q", errText)
	}
}

// codex 行样本：codex-cli 0.169.0 的实测输出（本机网关未启动；agent_message 行取自 --json 契约）。
func TestCodexParseAgentMessage(t *testing.T) {
	var got []exec.Event
	sink := sinkFunc(func(e exec.Event) { got = append(got, e) })
	fail := func(msg string) { t.Fatalf("不应报错：%s", msg) }
	noop := func(string) {}
	parseCodex([]byte(`{"type":"thread.started","thread_id":"abc"}`), sink, fail, noop)
	parseCodex([]byte(`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"pong"}}`), sink, fail, noop)
	parseCodex([]byte(`{"type":"turn.completed"}`), sink, fail, noop)
	if len(got) != 1 || got[0].Kind != exec.EventText || got[0].Text != "pong" {
		t.Fatalf("codex agent_message 解析不符：%#v", got)
	}
}

func TestCodexParseFailure(t *testing.T) {
	var errText string
	parseCodex([]byte(`{"type":"turn.failed","error":{"message":"sandbox denied"}}`),
		sinkFunc(func(exec.Event) {}), func(msg string) { errText = msg }, func(string) {})
	if errText != "sandbox denied" {
		t.Fatalf("turn.failed 未置错：%q", errText)
	}
}

// 实测的一次 502：5 条 error 重连提示之后才是 turn.failed；只有最后一条算失败，
// 重连提示只保留为诊断详情（进程非零退出且没有 stderr 时用）。
func TestCodexRetryNoticesAreNotFailures(t *testing.T) {
	var got []exec.Event
	var errText, diag string
	sink := sinkFunc(func(e exec.Event) { got = append(got, e) })
	fail := func(msg string) { errText = msg }
	note := func(msg string) { diag = msg }
	parseCodex([]byte(`{"type":"item.completed","item":{"id":"item_0","type":"error","message":"ignored setting"}}`), sink, fail, note)
	parseCodex([]byte(`{"type":"error","message":"Reconnecting... 1/5 (unexpected status 502 Bad Gateway)"}`), sink, fail, note)
	parseCodex([]byte(`{"type":"error","message":"Reconnecting... 5/5 (unexpected status 502 Bad Gateway)"}`), sink, fail, note)
	if errText != "" {
		t.Fatalf("重连提示不应置错：%q", errText)
	}
	if !strings.Contains(diag, "Reconnecting... 5/5") {
		t.Fatalf("应保留最后一条重连提示作为诊断：%q", diag)
	}
	parseCodex([]byte(`{"type":"turn.failed","error":{"message":"unexpected status 502 Bad Gateway"}}`), sink, fail, note)
	if errText != "unexpected status 502 Bad Gateway" {
		t.Fatalf("turn.failed 应置错：%q", errText)
	}
	if len(got) != 0 {
		t.Fatalf("失败路径不应产生事件：%#v", got)
	}
}

func TestCodexParseCommandExecution(t *testing.T) {
	var got []exec.Event
	sink := sinkFunc(func(e exec.Event) { got = append(got, e) })
	parseCodex([]byte(`{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"ls","aggregated_output":"a\nb\n","exit_code":0}}`), sink, func(string) {}, func(string) {})
	if len(got) != 1 || got[0].Kind != exec.EventToolResult || got[0].Input != "ls" || got[0].Output != "a\nb" || got[0].Err != "" {
		t.Fatalf("command_execution 解析不符：%#v", got)
	}
}

func TestPiParsePlainText(t *testing.T) {
	var got []exec.Event
	sink := sinkFunc(func(e exec.Event) { got = append(got, e) })
	p := &piParser{}
	p.add([]byte("第一行"))
	p.add([]byte(""))
	p.add([]byte("第二行"))
	p.done(sink)
	if len(got) != 1 || got[0].Kind != exec.EventText || got[0].Text != "第一行\n\n第二行" {
		t.Fatalf("纯文本适配器应整段输出一次：%#v", got)
	}
}

func TestBuildPromptDropsOldestHistory(t *testing.T) {
	history := []string{"最早的一段", strings.Repeat("x", promptHistoryLimit), "最新的一段"}
	prompt := buildPrompt(exec.Request{Instructions: "你是助手", History: history, Input: "做这件事"})
	if !strings.HasSuffix(prompt, "当前任务：\n做这件事") {
		t.Fatalf("当前任务必须完整保留：%q", prompt[len(prompt)-40:])
	}
	if !strings.Contains(prompt, "最新的一段") || !strings.Contains(prompt, historyOmitted) {
		t.Fatalf("预算内应保留最新一条并标注省略：%.120q", prompt)
	}
	if strings.Contains(prompt, "最早的一段") {
		t.Fatal("超预算时应丢掉最旧的对话")
	}
}
