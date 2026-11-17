package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiry163/reeve/internal/builtin"
)

func newEnvServer(t *testing.T) (*Bots, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	bs, err := OpenBots(Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bs.Close)
	return bs, bs.Handler(nil)
}

// envRequest 发一次 JSON 请求；body 为空时不带 Content-Type。
func envRequest(t *testing.T, h http.Handler, method, path, body string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w, w.Body.String()
}

func envVarsFrom(t *testing.T, body string) map[string]string {
	t.Helper()
	var payload struct {
		Vars map[string]string `json:"vars"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("响应不是 env JSON: %v: %s", err, body)
	}
	if payload.Vars == nil {
		t.Fatalf("vars 不应是 null: %s", body)
	}
	return payload.Vars
}

type envWarnings struct {
	OK       bool     `json:"ok"`
	Warnings []string `json:"warnings"`
}

// 非法变量名：400、错误里点名，且一个键都不写（先全量校验再落盘），也不写审计事件。
func TestEnvPutRejectsInvalidNameAtomically(t *testing.T) {
	bs, h := newEnvServer(t)
	if w, body := envRequest(t, h, "PUT", "/api/env", `{"vars":{"REEVE_HTTP_KEEP":"one"}}`); w.Code != http.StatusOK {
		t.Fatalf("初始化失败 %d: %s", w.Code, body)
	}
	before, err := os.ReadFile(filepath.Join(bs.main.cfg.DataDir, "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	w, body := envRequest(t, h, "PUT", "/api/env", `{"vars":{"REEVE_HTTP_KEEP":"two","BAD-NAME":"x"}}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法变量名应 400，得到 %d: %s", w.Code, body)
	}
	if !strings.Contains(body, "BAD-NAME") {
		t.Fatalf("错误信息应指出变量名: %s", body)
	}
	w, body = envRequest(t, h, "GET", "/api/env", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET 失败 %d: %s", w.Code, body)
	}
	vars := envVarsFrom(t, body)
	if vars["REEVE_HTTP_KEEP"] != "one" {
		t.Fatalf("非法 PUT 不应改动任何键，得到 %v", vars)
	}
	if _, ok := vars["BAD-NAME"]; ok {
		t.Fatalf("非法键不应落盘: %v", vars)
	}
	after, err := os.ReadFile(filepath.Join(bs.main.cfg.DataDir, "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("校验失败不应写 env 事件:\n之前: %s\n之后: %s", before, after)
	}
	if strings.Contains(string(after), "BAD-NAME") {
		t.Fatalf("非法变量名不应进入事件日志: %s", after)
	}
}

// 合法 PUT → GET 回读一致；PUT 响应带 ok 与空 warnings 数组。
func TestEnvPutRoundTrip(t *testing.T) {
	_, h := newEnvServer(t)
	w, body := envRequest(t, h, "PUT", "/api/env", `{"vars":{"REEVE_HTTP_A":"1","REEVE_HTTP_B":"two words"}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT 失败 %d: %s", w.Code, body)
	}
	var result envWarnings
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Warnings == nil || len(result.Warnings) != 0 {
		t.Fatalf("普通值的响应应 ok=true 且 warnings=[]: %s", body)
	}
	if !strings.Contains(body, `"warnings":[]`) {
		t.Fatalf("warnings 不能序列化成 null: %s", body)
	}
	_, body = envRequest(t, h, "GET", "/api/env", "")
	vars := envVarsFrom(t, body)
	if vars["REEVE_HTTP_A"] != "1" || vars["REEVE_HTTP_B"] != "two words" {
		t.Fatalf("回读不一致: %v", vars)
	}
}

// 整体替换：body 里没有的键被删除，并留下 env.unset 审计事件。
func TestEnvPutReplacesWholeSet(t *testing.T) {
	bs, h := newEnvServer(t)
	if w, body := envRequest(t, h, "PUT", "/api/env", `{"vars":{"REEVE_HTTP_A":"1","REEVE_HTTP_B":"2"}}`); w.Code != http.StatusOK {
		t.Fatalf("PUT 失败 %d: %s", w.Code, body)
	}
	if w, body := envRequest(t, h, "PUT", "/api/env", `{"vars":{"REEVE_HTTP_A":"1"}}`); w.Code != http.StatusOK {
		t.Fatalf("第二次 PUT 失败 %d: %s", w.Code, body)
	}
	_, body := envRequest(t, h, "GET", "/api/env", "")
	vars := envVarsFrom(t, body)
	if len(vars) != 1 || vars["REEVE_HTTP_A"] != "1" {
		t.Fatalf("REEVE_HTTP_B 应被整体替换删除: %v", vars)
	}
	raw, err := os.ReadFile(filepath.Join(bs.main.cfg.DataDir, "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"kind":"env.unset"`) || !strings.Contains(string(raw), "REEVE_HTTP_B") ||
		!strings.Contains(string(raw), `"layer":"instance"`) {
		t.Fatalf("应写入 layer=instance 的 env.unset 事件: %s", raw)
	}
}

// bot 级 PUT 走 Bot 方法：审计事件有 env.set/env.unset、含变量名、不含值。
func TestBotEnvPutAuditsWithoutValues(t *testing.T) {
	bs, h := newEnvServer(t)
	_, body := envRequest(t, h, "POST", "/api/bots", `{"name":"EnvChild"}`)
	var child BotProfile
	if err := json.Unmarshal([]byte(body), &child); err != nil || child.ID == "" {
		t.Fatalf("创建 Bot 失败: %s %v", body, err)
	}
	base := "/api/bots/" + child.ID + "/env"
	const value = "http://user:pass@127.0.0.1:7891"
	if w, body := envRequest(t, h, "PUT", base, `{"vars":{"HTTPS_PROXY":"`+value+`"}}`); w.Code != http.StatusOK {
		t.Fatalf("bot 级 PUT 失败 %d: %s", w.Code, body)
	}
	_, body = envRequest(t, h, "GET", base, "")
	vars := envVarsFrom(t, body)
	if vars["HTTPS_PROXY"] != value {
		t.Fatalf("bot 级回读不一致: %v", vars)
	}
	if w, body := envRequest(t, h, "PUT", base, `{"vars":{}}`); w.Code != http.StatusOK {
		t.Fatalf("清空失败 %d: %s", w.Code, body)
	}
	_, body = envRequest(t, h, "GET", base, "")
	if vars = envVarsFrom(t, body); len(vars) != 0 {
		t.Fatalf("清空后应为空: %v", vars)
	}
	raw, err := os.ReadFile(filepath.Join(bs.runtimes[child.ID].cfg.DataDir, "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"kind":"env.set"`, `"kind":"env.unset"`, "HTTPS_PROXY", `"layer":"bot"`, `"source":"user"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("审计事件缺少 %s: %s", want, raw)
		}
	}
	if strings.Contains(string(raw), "7891") || strings.Contains(string(raw), "pass") {
		t.Fatalf("审计事件不应包含值: %s", raw)
	}
}

// 疑似密钥：200 + warnings 非空（不拒绝）；普通值 warnings 为空数组。
func TestEnvPutWarnsOnSecretLookingValue(t *testing.T) {
	_, h := newEnvServer(t)
	w, body := envRequest(t, h, "PUT", "/api/env", `{"vars":{"REEVE_HTTP_TOKEN":"sk-abcdef0123456789"}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("疑似密钥不应被拒绝 %d: %s", w.Code, body)
	}
	var result envWarnings
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "REEVE_HTTP_TOKEN") {
		t.Fatalf("应返回含变量名的告警: %s", body)
	}
	// 值确实保存了，只是给了告警。
	_, body = envRequest(t, h, "GET", "/api/env", "")
	if vars := envVarsFrom(t, body); vars["REEVE_HTTP_TOKEN"] != "sk-abcdef0123456789" {
		t.Fatalf("疑似密钥值应仍然保存: %v", vars)
	}
	_, body = envRequest(t, h, "PUT", "/api/env", `{"vars":{"REEVE_HTTP_PLAIN":"hello"}}`)
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatal(err)
	}
	if result.Warnings == nil || len(result.Warnings) != 0 {
		t.Fatalf("普通值不应告警: %s", body)
	}
	if !strings.Contains(body, `"warnings":[]`) {
		t.Fatalf("空告警应是 []: %s", body)
	}
}

// env 文件损坏时，命令、工具与 HTTP 三个面的错误文案都不能带数据目录绝对路径。
func TestEnvErrorsRedactDataDirPaths(t *testing.T) {
	bs, h := newEnvServer(t)
	dataDir := bs.main.cfg.DataDir
	if err := os.WriteFile(filepath.Join(dataDir, "env"), []byte("NOT_A_PAIR\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "env.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	check := func(surface, msg string) {
		t.Helper()
		if msg == "" {
			t.Fatalf("%s 应返回错误文案", surface)
		}
		if strings.Contains(msg, dataDir) {
			t.Fatalf("%s 的错误文案泄露了绝对路径 %q：%s", surface, dataDir, msg)
		}
	}

	for _, line := range []string{"/env list", "/env set A=1", "/env set A=1 --global", "/env unset A", "/env unset A --global"} {
		res, ok := bs.main.CommandLine(context.Background(), line)
		if !ok || res.OK {
			t.Fatalf("%s 应失败：%+v", line, res)
		}
		check("命令 "+line, res.Text)
	}

	registry := builtin.NewRegistry(builtin.DefaultTools()...)
	for _, c := range []struct{ tool, args string }{
		{"list_env", `{}`},
		{"set_env", `{"name":"A","value":"1"}`},
		{"unset_env", `{"name":"A"}`},
	} {
		_, err := registry.Get(c.tool).Execute(context.Background(), builtin.ToolEnv{EnvStore: bs.main}, json.RawMessage(c.args))
		if err == nil {
			t.Fatalf("%s 应返回错误", c.tool)
		}
		check("工具 "+c.tool, err.Error())
	}

	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/env", ""},
		{"PUT", "/api/env", `{"vars":{"A":"1"}}`},
		{"GET", "/api/bots/main/env", ""},
		{"PUT", "/api/bots/main/env", `{"vars":{"A":"1"}}`},
	} {
		w, body := envRequest(t, h, c.method, c.path, c.body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s %s 应 400，得到 %d：%s", c.method, c.path, w.Code, body)
		}
		check("HTTP "+c.method+" "+c.path, body)
	}
}

// PUT /api/env 与 bot 级同端点都拒绝 {{secret:…}} 字面量；普通值照常保存。
func TestEnvPutRejectsSecretRefs(t *testing.T) {
	_, h := newEnvServer(t)
	for _, path := range []string{"/api/env", "/api/bots/main/env"} {
		w, body := envRequest(t, h, "PUT", path, `{"vars":{"DATABASE_URL":"postgres://u:{{secret:PG_PASS}}@db/app"}}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s 应拒绝密钥引用，得到 %d: %s", path, w.Code, body)
		}
		if !strings.Contains(body, "密钥请用设置页的密钥卡片，不要写进环境变量") {
			t.Fatalf("%s 的错误文案不对：%s", path, body)
		}
		if w, body := envRequest(t, h, "PUT", path, `{"vars":{"PLAIN_VALUE":"hello"}}`); w.Code != http.StatusOK {
			t.Fatalf("%s 的普通值不应受影响，得到 %d: %s", path, w.Code, body)
		}
	}
}

// body 里没有 vars 字段：400（与显式传 {} 清空区分开）。
func TestEnvPutRequiresVarsField(t *testing.T) {
	_, h := newEnvServer(t)
	if w, body := envRequest(t, h, "PUT", "/api/env", `{"other":1}`); w.Code != http.StatusBadRequest {
		t.Fatalf("缺少 vars 应 400，得到 %d: %s", w.Code, body)
	}
}

// bot 级同样先全量校验：两个键其中一个是非法名 → 400 且回读未变。
func TestBotEnvPutRejectsInvalidNameAtomically(t *testing.T) {
	_, h := newEnvServer(t)
	const base = "/api/bots/main/env"
	if w, body := envRequest(t, h, "PUT", base, `{"vars":{"REEVE_BOT_KEEP":"one","REEVE_BOT_OK":"x"}}`); w.Code != http.StatusOK {
		t.Fatalf("初始化失败 %d: %s", w.Code, body)
	}
	w, body := envRequest(t, h, "PUT", base, `{"vars":{"REEVE_BOT_KEEP":"two","BAD-NAME":"x"}}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(body, "BAD-NAME") {
		t.Fatalf("非法变量名应 400 并点名，得到 %d: %s", w.Code, body)
	}
	_, body = envRequest(t, h, "GET", base, "")
	vars := envVarsFrom(t, body)
	if len(vars) != 2 || vars["REEVE_BOT_KEEP"] != "one" || vars["REEVE_BOT_OK"] != "x" {
		t.Fatalf("非法 PUT 不应改动任何键，得到 %v", vars)
	}
}

// 实例级 env 文件按行存、会 trim 首尾空白：这类值必须 400 且不落盘，保证 GET 可回读一致。
func TestEnvPutRejectsInstanceWhitespace(t *testing.T) {
	_, h := newEnvServer(t)
	w, body := envRequest(t, h, "PUT", "/api/env", `{"vars":{"REEVE_HTTP_WS":" padded"}}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("首尾空白应 400，得到 %d: %s", w.Code, body)
	}
	if !strings.Contains(body, "REEVE_HTTP_WS") {
		t.Fatalf("错误信息应指出变量名: %s", body)
	}
	_, body = envRequest(t, h, "GET", "/api/env", "")
	if vars := envVarsFrom(t, body); len(vars) != 0 {
		t.Fatalf("非法值不应落盘: %v", vars)
	}
}
