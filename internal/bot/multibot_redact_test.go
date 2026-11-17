package bot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiry163/reeve/internal/auth"
	"github.com/kiry163/reeve/internal/exec"
	"github.com/kiry163/reeve/internal/exec/cli"
)

// 访问 token 是整台设备共用的（REEVE_HOME/token）。非默认 Bot 的数据目录在 bots/<id>/，
// 但脱敏也必须读到同一个 token，否则它的聊天记录、任务查询、工具输出全都不脱敏。
func TestNonDefaultBotRedactsAccessToken(t *testing.T) {
	dir := t.TempDir()
	ai := &fakeAI{agent: blockingAgent}
	cfg := Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws"),
		ChatProvider: ai, ChatModel: "chat", AgentProvider: ai, AgentModel: "agent"}
	bs, err := OpenBots(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()
	tok, _, err := auth.EnsureToken(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := bs.setProfile("", BotProfile{Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	child := bs.runtimes[p.ID]
	if child.cfg.TokenDir != cfg.DataDir {
		t.Fatalf("child TokenDir = %q, want %q", child.cfg.TokenDir, cfg.DataDir)
	}
	if _, err := child.Send("这是我的 token：" + tok); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(cfg.DataDir, "bots", p.ID, "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), tok) {
		t.Fatal("non-default bot persisted the access token in plaintext")
	}
	for _, m := range child.store.History(10) {
		if strings.Contains(m.Text, tok) {
			t.Fatalf("non-default bot History leaks token: %q", m.Text)
		}
	}
}

// 网关令牌是实例级的：非默认 Bot 用自己的脱敏器和事件库，但经它的外部任务签发的
// pi 令牌同样不能明文落进它的 stream.jsonl / SSE。
func TestNonDefaultBotRedactsGatewayToken(t *testing.T) {
	dir := t.TempDir()
	bin := t.TempDir()
	writeFakeCLI(t, bin, "pi", "#!/bin/sh\necho \"token=$REEVE_GATEWAY_TOKEN\"\n")
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NVM_DIR", "")
	cfg := Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws"),
		ChatProvider: &fakeAI{}, ChatModel: "chat", AgentProvider: &fakeAI{}, AgentModel: "agent",
		StallTimeout: time.Minute}
	bs, err := OpenBots(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()
	key := "sk-up"
	if _, err := bs.main.saveChannels(APISettings{Providers: []APIChannel{
		{ID: "up", Name: "Up", BaseURL: "http://127.0.0.1:9/v1", Enabled: true, APIKey: &key, Models: []string{"m1"}},
	}, DefaultProviderID: "up", Model: "m1"}); err != nil {
		t.Fatal(err)
	}
	p, err := bs.setProfile("", BotProfile{Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	child := bs.runtimes[p.ID]
	if child.redactor.gateway != bs.main.redactor.gateway {
		t.Fatal("非默认 Bot 没有共享网关令牌集合")
	}
	if err := child.SaveExecutors(Executors{Allowed: []string{string(exec.KindBuiltin), "pi"}, Default: "pi",
		CLIs: map[string]cli.Options{"pi": {Auth: cli.AuthChannel, Channel: "up", Model: "m1"}}}); err != nil {
		t.Fatal(err)
	}
	id, err := child.CreateTask("跑", "", TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "pi task completed", func() bool {
		rec, ok := child.store.Task(id)
		return ok && rec.Status == "completed"
	})
	rec, _ := child.store.Task(id)
	if strings.Contains(rec.Output, "reeve-gw-") || !strings.Contains(rec.Output, gatewayMask) {
		t.Fatalf("非默认 Bot 的输出没有脱敏网关令牌：%q", rec.Output)
	}
	raw, err := os.ReadFile(filepath.Join(cfg.DataDir, "bots", p.ID, "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "reeve-gw-") {
		t.Fatal("non-default bot persisted the gateway token in plaintext")
	}
	if !strings.Contains(string(raw), gatewayMask) {
		t.Fatalf("stream.jsonl 里应看到脱敏占位：%s", raw)
	}
}
