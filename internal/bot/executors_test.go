package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kiry163/reeve/internal/exec/cli"
)

func TestSaveExecutorsRejectsUnknownKind(t *testing.T) {
	b := newBotWith(t, t.TempDir(), nil, nil)
	defer b.Close()

	err := b.SaveExecutors(Executors{Allowed: []string{"builtin", "nope"}, Default: "builtin"})
	if err == nil || !strings.Contains(err.Error(), "nope") || !strings.Contains(err.Error(), "codex") {
		t.Fatalf("未知执行器应被拒绝、点名并列出可用值，得到 %v", err)
	}
	if _, statErr := os.Stat(b.executorsPath()); !os.IsNotExist(statErr) {
		t.Fatal("校验失败不应落盘")
	}
	if len(b.store.Since(0)) != 0 {
		t.Fatal("校验失败不应写事件")
	}
}

func TestSaveExecutorsRequiresBuiltinAndDefaultInAllowed(t *testing.T) {
	b := newBotWith(t, t.TempDir(), nil, nil)
	defer b.Close()

	if err := b.SaveExecutors(Executors{Allowed: []string{"codex"}, Default: "codex"}); err == nil {
		t.Fatal("白名单必须包含 builtin")
	}
	if err := b.SaveExecutors(Executors{Allowed: []string{"builtin", "codex"}, Default: "omp"}); err == nil {
		t.Fatal("默认执行器必须在白名单内")
	}
	if got := b.Executors(); !slices.Equal(got.Allowed, []string{"builtin"}) || got.Default != "builtin" {
		t.Fatalf("校验失败后旧值应保持不变：%+v", got)
	}
	if _, err := os.Stat(b.executorsPath()); !os.IsNotExist(err) {
		t.Fatal("校验失败不应落盘")
	}
}

func TestExecutorsMigrationFromAgentConnection(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := agentConnection{CLI: "pi", Options: cli.Options{Auth: "channel", Model: "x"}}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "agent-connection.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{DataDir: dataDir, Workspace: filepath.Join(dir, "ws")}
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := b.Executors()
	if !slices.Equal(got.Allowed, []string{"builtin", "pi"}) || got.Default != "pi" {
		t.Fatalf("迁移结果不对：%+v", got)
	}
	if got.CLIs["pi"].Model != "x" || got.CLIs["pi"].Auth != cli.AuthChannel {
		t.Fatalf("应保留旧 CLI 选项：%+v", got.CLIs)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "agent-connection.json")); !os.IsNotExist(err) {
		t.Fatal("迁移后旧文件应删除")
	}
	info, err := os.Stat(filepath.Join(dataDir, "executors.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("executors.json 权限应为 0600，得到 %o", info.Mode().Perm())
	}
	b.Close()

	// 二次读取只读新文件，结果不变（幂等）。
	again, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	got2 := again.Executors()
	if !slices.Equal(got2.Allowed, got.Allowed) || got2.Default != got.Default || got2.CLIs["pi"] != got.CLIs["pi"] {
		t.Fatalf("二次读取结果不一致：%+v vs %+v", got2, got)
	}
}

func TestAgentCommandSwitchesDefaultWithinWhitelist(t *testing.T) {
	b := newBotWith(t, t.TempDir(), nil, nil)
	defer b.Close()
	if err := b.SaveExecutors(Executors{Allowed: []string{"builtin", "codex"}, Default: "builtin"}); err != nil {
		t.Fatal(err)
	}

	res, ok := b.CommandLine(context.Background(), "/agent")
	if !ok || !res.OK || !strings.Contains(res.Text, "默认执行器：builtin") || !strings.Contains(res.Text, "白名单：builtin、codex") {
		t.Fatalf("/agent 应显示默认与白名单：%+v", res)
	}
	res, _ = b.CommandLine(context.Background(), "/agent list")
	if !res.OK || !strings.Contains(res.Text, "白名单：builtin、codex") || strings.Contains(res.Text, "默认执行器") {
		t.Fatalf("/agent list 只列白名单：%+v", res)
	}
	res, ok = b.CommandLine(context.Background(), "/agent codex")
	if !ok || !res.OK || !strings.Contains(res.Text, "codex") || b.DefaultExecutor() != "codex" || b.selectedCLI() != "codex" {
		t.Fatalf("/agent codex 应成功：%+v", res)
	}
	res, _ = b.CommandLine(context.Background(), "/agent omp")
	if res.OK || !strings.Contains(res.Text, "白名单") {
		t.Fatalf("白名单外应被拒绝并提示白名单：%+v", res)
	}
	if b.DefaultExecutor() != "codex" {
		t.Fatal("被拒绝的切换不应改变默认值")
	}
	res, _ = b.CommandLine(context.Background(), "/agent builtin")
	if !res.OK || b.DefaultExecutor() != "builtin" || b.selectedCLI() != "" {
		t.Fatalf("/agent builtin 应真的切回内置：%+v default=%s", res, b.DefaultExecutor())
	}
}

func TestExecutorsHTTPGetPutAndValidation(t *testing.T) {
	_, h := newEnvServer(t)
	const path = "/api/bots/main/executors"

	w, body := envRequest(t, h, "GET", path, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET 失败：%d %s", w.Code, body)
	}
	var got Executors
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("GET 不是 Executors JSON：%v %s", err, body)
	}
	if !slices.Equal(got.Allowed, []string{"builtin"}) || got.Default != "builtin" {
		t.Fatalf("初始配置不对：%+v", got)
	}
	if !strings.Contains(body, `"allowed"`) || !strings.Contains(body, `"default"`) || !strings.Contains(body, `"clis"`) {
		t.Fatalf("GET 形状缺少 allowed/default/clis：%s", body)
	}

	// 校验失败：400，且旧值不变。
	w, body = envRequest(t, h, "PUT", path, `{"allowed":["builtin","nope"],"default":"builtin"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(body, "nope") {
		t.Fatalf("未知 kind 应 400 并点名：%d %s", w.Code, body)
	}
	_, body = envRequest(t, h, "GET", path, "")
	if err := json.Unmarshal([]byte(body), &got); err != nil || !slices.Equal(got.Allowed, []string{"builtin"}) || got.Default != "builtin" {
		t.Fatalf("校验失败后旧值应不变：%s", body)
	}
	if w, _ = envRequest(t, h, "PUT", path, `{"allowed":["codex"],"default":"codex"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("缺少 builtin 应 400，得到 %d", w.Code)
	}
	if w, _ = envRequest(t, h, "PUT", path, `{"allowed":["builtin","codex"],"default":"omp"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("默认值不在白名单应 400，得到 %d", w.Code)
	}

	// 成功后 GET 能读到。
	w, body = envRequest(t, h, "PUT", path, `{"allowed":["builtin","codex"],"default":"codex","clis":{"codex":{"cli_model":"gpt-5"}}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT 失败：%d %s", w.Code, body)
	}
	var saved Executors
	if err := json.Unmarshal([]byte(body), &saved); err != nil {
		t.Fatalf("PUT 响应不是 Executors JSON：%v %s", err, body)
	}
	if !slices.Equal(saved.Allowed, []string{"builtin", "codex"}) || saved.Default != "codex" || saved.CLIs["codex"].Model != "gpt-5" {
		t.Fatalf("PUT 响应应是保存后的新值：%s", body)
	}
	_, body = envRequest(t, h, "GET", path, "")
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Allowed, []string{"builtin", "codex"}) || got.Default != "codex" || got.CLIs["codex"].Model != "gpt-5" {
		t.Fatalf("GET 未读到新值：%s", body)
	}
}

func TestSaveExecutorsKeepsOptionsForRemovedKinds(t *testing.T) {
	b := newBotWith(t, t.TempDir(), nil, nil)
	defer b.Close()
	if err := b.SaveExecutors(Executors{Allowed: []string{"builtin", "codex"}, Default: "codex",
		CLIs: map[string]cli.Options{"codex": {Model: "gpt-5"}}}); err != nil {
		t.Fatal(err)
	}
	// 取消勾选 codex，但客户端仍带着完整 clis 快照：选项要保留。
	ex := b.Executors()
	ex.Allowed, ex.Default = []string{"builtin"}, "builtin"
	if err := b.SaveExecutors(ex); err != nil {
		t.Fatal(err)
	}
	if got := b.Executors(); got.CLIs["codex"].Model != "gpt-5" {
		t.Fatalf("移除白名单条目时不应丢弃已存选项：%+v", got.CLIs)
	}
	// 重新勾选后选项仍在。
	ex = b.Executors()
	ex.Allowed, ex.Default = []string{"builtin", "codex"}, "codex"
	if err := b.SaveExecutors(ex); err != nil {
		t.Fatal(err)
	}
	if got := b.Executors(); got.CLIs["codex"].Model != "gpt-5" {
		t.Fatalf("重新添加后选项丢失：%+v", got.CLIs)
	}
}

func TestUpdateExecutorsConcurrentRMWKeepsBothKinds(t *testing.T) {
	b := newBotWith(t, t.TempDir(), nil, nil)
	defer b.Close()
	var wg sync.WaitGroup
	for _, kind := range []string{"codex", "omp"} {
		wg.Add(1)
		go func(kind string) {
			defer wg.Done()
			err := b.updateExecutors(func(ex *Executors) error {
				if !slices.Contains(ex.Allowed, kind) {
					ex.Allowed = append(ex.Allowed, kind)
				}
				return nil
			})
			if err != nil {
				t.Errorf("添加 %s 失败：%v", kind, err)
			}
		}(kind)
	}
	wg.Wait()
	got := b.Executors()
	if !slices.Contains(got.Allowed, "codex") || !slices.Contains(got.Allowed, "omp") {
		t.Fatalf("并发 RMW 丢了白名单条目：%+v", got.Allowed)
	}
	raw, err := os.ReadFile(b.executorsPath())
	if err != nil {
		t.Fatal(err)
	}
	var onDisk Executors
	if err := json.Unmarshal(raw, &onDisk); err != nil || !slices.Contains(onDisk.Allowed, "codex") || !slices.Contains(onDisk.Allowed, "omp") {
		t.Fatalf("并发 RMW 落盘丢了条目：%s", raw)
	}
}

func TestLoadExecutorsDropsUnknownKind(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := `{"allowed":["builtin","nope","codex"],"default":"nope","clis":{"nope":{"cli_model":"x"},"codex":{"cli_model":"gpt-5"}}}`
	if err := os.WriteFile(filepath.Join(dataDir, "executors.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := New(Config{DataDir: dataDir, Workspace: filepath.Join(dir, "ws")})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	got := b.Executors()
	if !slices.Equal(got.Allowed, []string{"builtin", "codex"}) || got.Default != "builtin" {
		t.Fatalf("未知 kind 应被软降级：%+v", got)
	}
	if _, ok := got.CLIs["nope"]; ok || got.CLIs["codex"].Model != "gpt-5" {
		t.Fatalf("clis 处理不对：%+v", got.CLIs)
	}
	var updated, dropped bool
	for _, e := range b.store.Since(0) {
		switch e.Kind {
		case "executors.updated":
			updated = true
			if strings.Contains(string(e.Data), "nope") {
				t.Fatalf("executors.updated 不应包含未知 kind：%s", e.Data)
			}
		case "executors.dropped":
			dropped = true
			if !strings.Contains(string(e.Data), "nope") {
				t.Fatalf("executors.dropped 应点名未知 kind：%s", e.Data)
			}
		}
	}
	if !updated || !dropped {
		t.Fatalf("应同时写 executors.updated 与 executors.dropped：updated=%v dropped=%v", updated, dropped)
	}
	// 软降级后的配置要写回磁盘，下次启动不再重复提示。
	onDisk, err := os.ReadFile(filepath.Join(dataDir, "executors.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(onDisk), "nope") {
		t.Fatalf("未知 kind 没有从磁盘清掉：%s", onDisk)
	}
}

func TestExecutorsMigrationDropsUnknownCLI(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "agent-connection.json"), []byte(`{"cli":"claude","cli_model":"opus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := New(Config{DataDir: dataDir, Workspace: filepath.Join(dir, "ws")})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	got := b.Executors()
	if !slices.Equal(got.Allowed, []string{"builtin"}) || got.Default != "builtin" {
		t.Fatalf("旧配置里的未知 CLI 应软降级：%+v", got)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "agent-connection.json")); !os.IsNotExist(err) {
		t.Fatal("迁移后旧文件应删除")
	}
	var dropped bool
	for _, e := range b.store.Since(0) {
		if e.Kind == "executors.dropped" && strings.Contains(string(e.Data), "claude") {
			dropped = true
		}
	}
	if !dropped {
		t.Fatal("迁移丢弃未知 CLI 时应写 executors.dropped")
	}
}

func TestExecutorsUpdatedEventOnlyAllowedAndDefault(t *testing.T) {
	b := newBotWith(t, t.TempDir(), nil, nil)
	defer b.Close()
	if err := b.SaveExecutors(Executors{Allowed: []string{"builtin", "codex"}, Default: "codex",
		CLIs: map[string]cli.Options{"codex": {Model: "secret-model"}}}); err != nil {
		t.Fatal(err)
	}
	var events []Event
	for _, e := range b.store.Since(0) {
		if e.Kind == "executors.updated" {
			events = append(events, e)
		}
	}
	if len(events) != 1 {
		t.Fatalf("应恰好写一条 executors.updated，得到 %d", len(events))
	}
	var data map[string]any
	if err := json.Unmarshal(events[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data) != 2 {
		t.Fatalf("事件只能记 allowed/default：%s", events[0].Data)
	}
	allowed, ok := data["allowed"].([]any)
	if !ok || len(allowed) != 2 || allowed[0] != "builtin" || allowed[1] != "codex" || data["default"] != "codex" {
		t.Fatalf("事件内容不对：%s", events[0].Data)
	}
	if strings.Contains(string(events[0].Data), "secret-model") || strings.Contains(string(events[0].Data), "clis") {
		t.Fatalf("事件不应记录选项细节：%s", events[0].Data)
	}
}

// PUT /api/executors 的失败文案不能带数据目录的绝对路径（复用既有脱敏助手）。
func TestExecutorsHTTPErrorRedactsDataDir(t *testing.T) {
	bs, h := newEnvServer(t)
	dir := bs.main.cfg.DataDir
	// 让落盘失败：executors.json 变成目录，rename 一定报错并把绝对路径带进错误。
	if err := os.MkdirAll(filepath.Join(dir, "executors.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	w, body := envRequest(t, h, "PUT", "/api/bots/main/executors", `{"allowed":["builtin"],"default":"builtin"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("落盘失败应返回错误：%d %s", w.Code, body)
	}
	if strings.Contains(body, dir) {
		t.Fatalf("错误文案泄漏了数据目录绝对路径：%s", body)
	}
	if !strings.Contains(body, "executors.json") {
		t.Fatalf("错误文案应保留相对路径便于排查：%s", body)
	}
}
