package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiry163/reeve/internal/exec"
)

// CLI 子进程的环境同样以 EnvList 完整替换；reeve 自身的密钥仍被过滤。
func TestCommandEnvUsesEnvList(t *testing.T) {
	process := []string{"REEVE_TEST_ONLY_PROCESS=x", "PATH=/usr/bin:/bin"}
	env := CommandEnv(process, []string{"PATH=/usr/bin:/bin", "REEVE_CLI_TEST=ok", "REEVE_MASTER_KEY=leak"},
		exec.Kind("claude"), AuthSubscription)
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "REEVE_CLI_TEST=ok") {
		t.Fatalf("CLI 环境未使用 EnvList：%s", joined)
	}
	if strings.Contains(joined, "REEVE_TEST_ONLY_PROCESS") {
		t.Fatalf("EnvList 非 nil 时应完整替换继承环境：%s", joined)
	}
	if strings.Contains(joined, "REEVE_MASTER_KEY") {
		t.Fatalf("reeve 密钥不应传给 CLI：%s", joined)
	}
}

// envList 为 nil 时 CLI 仍继承进程环境（既有行为不变）。
func TestCommandEnvNilKeepsInherited(t *testing.T) {
	env := CommandEnv([]string{"PATH=/usr/bin:/bin", "HTTPS_PROXY=http://127.0.0.1:7890"}, nil,
		exec.Kind("claude"), AuthSubscription)
	if joined := strings.Join(env, "\n"); !strings.Contains(joined, "HTTPS_PROXY=http://127.0.0.1:7890") {
		t.Fatalf("EnvList 为 nil 时 CLI 未继承进程环境：%s", joined)
	}
}

// auth=env 时 SafeEnviron 已剥掉厂商 Key，要从进程环境补齐该 CLI 自己的变量。
func TestCommandEnvAuthEnvKeepsVendorKeys(t *testing.T) {
	proc := []string{"PATH=/usr/bin:/bin", "OPENAI_API_KEY=sk-proc", "REEVE_MASTER_KEY=mm"}
	env := CommandEnv(proc, []string{"PATH=/usr/bin:/bin"}, exec.KindCodex, AuthEnv)
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "OPENAI_API_KEY=sk-proc") {
		t.Fatalf("auth=env 应保留进程里的厂商 Key：%s", joined)
	}
	if strings.Contains(joined, "REEVE_MASTER_KEY") {
		t.Fatalf("reeve 密钥不应传给 CLI：%s", joined)
	}
}

// 订阅模式仍然剔除厂商 Key，即使它在进程环境里。
func TestCommandEnvSubscriptionDropsVendorKeys(t *testing.T) {
	proc := []string{"PATH=/usr/bin:/bin", "OPENAI_API_KEY=sk-proc"}
	env := CommandEnv(proc, []string{"PATH=/usr/bin:/bin"}, exec.KindCodex, AuthSubscription)
	if joined := strings.Join(env, "\n"); strings.Contains(joined, "OPENAI_API_KEY") {
		t.Fatalf("订阅模式不应传厂商 Key：%s", joined)
	}
}

// 层里显式设置的厂商变量优先于进程值。
func TestCommandEnvLayerVendorValueWins(t *testing.T) {
	proc := []string{"PATH=/usr/bin:/bin", "OPENAI_API_KEY=sk-proc"}
	env := CommandEnv(proc, []string{"PATH=/usr/bin:/bin", "OPENAI_API_KEY=sk-layer"}, exec.KindCodex, AuthEnv)
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "OPENAI_API_KEY=sk-layer") || strings.Contains(joined, "sk-proc") {
		t.Fatalf("层里的值应优先：%s", joined)
	}
	if strings.Count(joined, "OPENAI_API_KEY=") != 1 {
		t.Fatalf("同名变量应只出现一次：%s", joined)
	}
}

// M3 的注册表只有 builtin/codex/pi/omp：已退场的 kind 连选项校验都过不了
// （配置迁移在到达这里之前就会按注册表丢弃它们）。
func TestUnknownKindsRejected(t *testing.T) {
	for _, kind := range []string{"claude", "grok", "agy"} {
		if _, err := ValidateOptions(exec.Kind(kind), Options{Model: "x"}); err == nil {
			t.Fatalf("%s 不该再被接受", kind)
		}
	}
}

type stubGateway struct{ revoked int }

func (g *stubGateway) Issue(channel, model string, _ time.Duration) (string, string, func(), error) {
	return "http://127.0.0.1:9/v1", "tok-" + channel + "-" + model, func() { g.revoked++ }, nil
}

func useStubGateway(t *testing.T) *stubGateway {
	t.Helper()
	g := &stubGateway{}
	SetGateway(g)
	t.Cleanup(func() { SetGateway(nil) })
	return g
}

// Pi 的临时配置：只指向本机网关，Key 写成环境变量引用；运行结束后令牌作废、目录删掉。
func TestPiChannelEnv(t *testing.T) {
	if _, _, err := ChannelEnv(nil, Options{}); err == nil {
		t.Fatal("Pi without a channel must fail")
	}
	g := useStubGateway(t)
	base := []string{"PATH=/bin", "PI_CODING_AGENT_DIR=/old", "REEVE_GATEWAY_TOKEN=old"}
	env, cleanup, err := ChannelEnv(base, Options{Channel: "relay", Model: "claude-x"})
	if err != nil {
		t.Fatal(err)
	}
	var dir, token string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PI_CODING_AGENT_DIR="); ok {
			dir = v
		}
		if v, ok := strings.CutPrefix(kv, "REEVE_GATEWAY_TOKEN="); ok {
			token = v
		}
	}
	if token != "tok-relay-claude-x" || strings.Contains(strings.Join(env, "\n"), "/old") {
		t.Fatalf("env %v: old config must be replaced", env)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"baseUrl": "http://127.0.0.1:9/v1"`, `"apiKey": "$REEVE_GATEWAY_TOKEN"`, `"id": "claude-x"`, `"api": "openai-completions"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("models.json missing %s:\n%s", want, raw)
		}
	}
	if strings.Contains(string(raw), token) {
		t.Fatal("token must not be written into the config file")
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) || g.revoked != 1 {
		t.Fatalf("cleanup: dir still there (%v) or token not revoked (%d)", err, g.revoked)
	}
}

// npm 装的 CLI 用 `#!/usr/bin/env node` 启动：node 和 CLI 在同一个不在 PATH 里的目录，也要能跑。
func TestPiRunnerFindsInterpreterNextToCLI(t *testing.T) {
	home := fakeHome(t)
	bin := filepath.Join(home, ".nvm/versions/node/v22.23.5/bin")
	writeExe(t, filepath.Join(bin, "node"), "#!/bin/sh\ncat >/dev/null\necho pi-ok\n")
	writeExe(t, filepath.Join(bin, "pi"), "#!/usr/bin/env node\n")
	runner := &piRunner{look: func(exec.Kind) (string, []string, error) {
		return filepath.Join(bin, "pi"), nil, nil
	}}
	h, err := runner.Start(context.Background(), exec.Request{TaskID: "t", Input: "hello", Workspace: t.TempDir(),
		Env: CommandEnv(os.Environ(), nil, exec.KindPi, AuthSubscription)}, sinkFunc(func(exec.Event) {}))
	if err != nil {
		t.Fatal(err)
	}
	res := <-h.Done()
	if res.Status != "completed" || strings.TrimSpace(res.Output) != "pi-ok" {
		t.Fatalf("pi runner = %+v", res)
	}
}

// 假 CLI 回显 Request.Env 里的变量，证明外部执行器拿到的是过滤后的环境。
func TestPiRunnerUsesRequestEnv(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "pi")
	writeExe(t, script, "#!/bin/sh\nprintf '%s' \"$REEVE_CLI_TEST\"\n")
	runner := &piRunner{look: func(exec.Kind) (string, []string, error) { return script, nil, nil }}
	env := CommandEnv([]string{"PATH=/usr/bin:/bin", "REEVE_CLI_TEST=ok", "REEVE_MASTER_KEY=x"},
		[]string{"PATH=/usr/bin:/bin", "REEVE_CLI_TEST=ok", "REEVE_MASTER_KEY=x"}, exec.KindPi, AuthSubscription)
	h, err := runner.Start(context.Background(), exec.Request{TaskID: "t", Input: "hi", Workspace: dir, Env: env},
		sinkFunc(func(exec.Event) {}))
	if err != nil {
		t.Fatal(err)
	}
	res := <-h.Done()
	if res.Status != "completed" || strings.TrimSpace(res.Output) != "ok" {
		t.Fatalf("CLI 未使用 Request.Env：%+v", res)
	}
}
