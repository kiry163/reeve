package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// GET /api/commands 是全局静态表：至少 10 条、含 env 与 approve，aliases 一律是数组。
func TestCommandsList(t *testing.T) {
	_, h := newEnvServer(t)
	w, body := envRequest(t, h, "GET", "/api/commands", "")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200：%s", w.Code, body)
	}
	var payload commandList
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("响应不是命令列表 JSON: %v: %s", err, body)
	}
	if len(payload.Commands) < 10 {
		t.Fatalf("命令数 = %d，期望至少 10：%s", len(payload.Commands), body)
	}
	seen := make(map[string]bool, len(payload.Commands))
	for _, c := range payload.Commands {
		if c.Name == "" || c.Summary == "" || c.Usage == "" {
			t.Errorf("命令定义缺字段：%+v", c)
		}
		if c.Aliases == nil {
			t.Errorf("命令 %s 的 aliases 是 null，应为 []", c.Name)
		}
		seen[c.Name] = true
	}
	for _, want := range []string{"env", "approve"} {
		if !seen[want] {
			t.Errorf("命令列表里没有 %s：%s", want, body)
		}
	}
}

// POST /api/bots/{id}/commands 执行 /help：200 且 text 里带命令清单。
func TestCommandRunHelp(t *testing.T) {
	bs, h := newEnvServer(t)
	w, body := envRequest(t, h, "POST", "/api/bots/main/commands", `{"line":"/help"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200：%s", w.Code, body)
	}
	var res commandResult
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatalf("响应不是命令结果 JSON: %v: %s", err, body)
	}
	if !res.OK {
		t.Errorf("ok = false，期望 true：%s", body)
	}
	if !strings.Contains(res.Text, "/help") {
		t.Errorf("text 里没有 /help：%q", res.Text)
	}
	// command.result 只由 CommandLine 写一次，HTTP 层不重复写。
	n := 0
	for _, e := range bs.main.store.Since(0) {
		if e.Kind == "command.result" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("command.result 事件数 = %d，期望 1", n)
	}
}

// 普通文本、空行与缺 line 字段都不是命令：400，且说明 line 必须以 / 开头。
func TestCommandRunRejectsNonCommand(t *testing.T) {
	_, h := newEnvServer(t)
	for _, body := range []string{`{"line":"你好"}`, `{"line":""}`, `{"line":"   "}`, `{}`} {
		w, resp := envRequest(t, h, "POST", "/api/bots/main/commands", body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s 的状态码 = %d，期望 400：%s", body, w.Code, resp)
		}
		if !strings.Contains(resp, "line 必须以 / 开头") {
			t.Errorf("%s 的错误说明不对：%s", body, resp)
		}
	}
}

// 超长命令行：HTTP 回 400，CommandLine 回 OK=false，事件里的命令名截断到 32 字节。
func TestCommandLineLengthLimit(t *testing.T) {
	bs, h := newEnvServer(t)
	line := "/" + strings.Repeat("x", maxCommandLine)
	payload, _ := json.Marshal(map[string]string{"line": line})
	w, body := envRequest(t, h, "POST", "/api/bots/main/commands", string(payload))
	if w.Code != http.StatusBadRequest || !strings.Contains(body, "太长") {
		t.Fatalf("超长命令应 400 并提示长度，得到 %d：%s", w.Code, body)
	}

	res, ok := bs.main.CommandLine(context.Background(), line)
	if !ok || res.OK || !strings.Contains(res.Text, "最多 4096 字节") {
		t.Fatalf("CommandLine 应返回 OK=false 的长度提示：%+v", res)
	}
	var seen int
	for _, e := range bs.main.store.Since(0) {
		if e.Kind != "command.result" {
			continue
		}
		seen++
		var data struct{ Command, Text string }
		if err := json.Unmarshal(e.Data, &data); err != nil {
			t.Fatal(err)
		}
		if len(data.Command) > maxCommandName {
			t.Fatalf("事件里的命令名应截断到 %d 字节，得到 %d: %q", maxCommandName, len(data.Command), data.Command)
		}
		if len(data.Text) > 1024 {
			t.Fatalf("超长命令的提示文本不应很长：%d 字节", len(data.Text))
		}
	}
	if seen != 1 {
		t.Fatalf("应只写一条 command.result，得到 %d", seen)
	}
	if name := commandName("/" + strings.Repeat("y", 100)); len(name) > maxCommandName {
		t.Fatalf("commandName 截断失败：%d 字节", len(name))
	}
}

// 执行命令是 bot 级行为：顶层没有 POST /api/commands。
func TestCommandsPostIsBotScoped(t *testing.T) {
	_, h := newEnvServer(t)
	w, resp := envRequest(t, h, "POST", "/api/commands", `{"line":"/help"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("顶层 POST /api/commands 状态码 = %d，期望 404：%s", w.Code, resp)
	}
}
