package bot

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/kiry163/reeve/internal/envstore"
)

func TestSetBotEnvWritesEventWithoutValue(t *testing.T) {
	b := newBot(t, t.TempDir(), nil)
	if err := b.SetBotEnv("HTTPS_PROXY", "http://127.0.0.1:7890", "chat"); err != nil {
		t.Fatal(err)
	}
	got, err := b.BotEnv()
	if err != nil || got["HTTPS_PROXY"] != "http://127.0.0.1:7890" {
		t.Fatalf("bot env 未写入：%v %v", got, err)
	}
	raw, err := os.ReadFile(filepath.Join(b.cfg.DataDir, "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "7890") {
		t.Fatal("env.set 事件不应包含值")
	}
	if !strings.Contains(string(raw), `"kind":"env.set"`) || !strings.Contains(string(raw), "HTTPS_PROXY") {
		t.Fatal("应写入含变量名的 env.set 事件")
	}
}

func TestTaskEnvLayers(t *testing.T) {
	b := newBot(t, t.TempDir(), nil)
	t.Setenv("REEVE_TEST_BASE", "base")
	_ = b.SetBotEnv("REEVE_TEST_BOT", "bot", "user")
	joined := strings.Join(b.TaskEnv(), "\n")
	for _, want := range []string{"REEVE_TEST_BASE=base", "REEVE_TEST_BOT=bot"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少 %s\n%s", want, joined)
		}
	}
}

// 实例级 <DataDir>/env 参与合并，bot 级覆盖同名实例级变量。
func TestTaskEnvInstanceLayer(t *testing.T) {
	b := newBot(t, t.TempDir(), nil)
	instance := "REEVE_TEST_LAYER=instance\nREEVE_TEST_ONLY_INSTANCE=inst\n"
	if err := os.WriteFile(filepath.Join(b.cfg.DataDir, "env"), []byte(instance), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := b.SetBotEnv("REEVE_TEST_LAYER", "bot", "user"); err != nil {
		t.Fatal(err)
	}
	instVars, botVars, err := b.View()
	if err != nil || instVars["REEVE_TEST_LAYER"] != "instance" || botVars["REEVE_TEST_LAYER"] != "bot" {
		t.Fatalf("View 读错了：%v %v %v", instVars, botVars, err)
	}
	joined := strings.Join(b.TaskEnv(), "\n")
	for _, want := range []string{"REEVE_TEST_LAYER=bot", "REEVE_TEST_ONLY_INSTANCE=inst"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少 %s\n%s", want, joined)
		}
	}
	merged, err := envstore.Overlay(b.TaskEnv(), map[string]string{"REEVE_TEST_LAYER": "call"})
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(merged, "\n")
	if !strings.Contains(joined, "REEVE_TEST_LAYER=call") || strings.Contains(joined, "REEVE_TEST_LAYER=bot") {
		t.Fatalf("调用级没有覆盖 bot 级\n%s", joined)
	}
	if strings.Count(joined, "REEVE_TEST_LAYER=") != 1 {
		t.Fatalf("同名变量应只出现一次\n%s", joined)
	}
}

// base 用 SafeEnviron：进程里的密钥不进执行器环境；用户显式写进实例级/bot 级的仍保留。
func TestTaskEnvDropsHiddenSecretsUnlessExplicit(t *testing.T) {
	b := newBot(t, t.TempDir(), nil)
	t.Setenv("REEVE_MASTER_KEY", "leaked-from-process")
	if joined := strings.Join(b.TaskEnv(), "\n"); strings.Contains(joined, "REEVE_MASTER_KEY=") {
		t.Fatalf("REEVE_MASTER_KEY 不应出现在执行器环境\n%s", joined)
	}
	if err := b.SetBotEnv("REEVE_MASTER_KEY", "explicit-bot", "user"); err != nil {
		t.Fatal(err)
	}
	if err := b.SetInstanceEnv("OPENAI_API_KEY", "explicit-instance", "user"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(b.TaskEnv(), "\n")
	for _, want := range []string{"REEVE_MASTER_KEY=explicit-bot", "OPENAI_API_KEY=explicit-instance"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("显式写入的变量应保留 %s\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "leaked-from-process") {
		t.Fatalf("进程里的密钥值泄漏\n%s", joined)
	}
}

// SetEnv/UnsetEnv 按 global 分发：true → 实例级文件与 layer=instance 事件，false → bot 级。
func TestSetEnvGlobalDispatch(t *testing.T) {
	b := newBot(t, t.TempDir(), nil)
	if err := b.SetEnv("REEVE_TEST_GLOBAL", "inst", true); err != nil {
		t.Fatal(err)
	}
	if err := b.SetEnv("REEVE_TEST_LOCAL", "bot", false); err != nil {
		t.Fatal(err)
	}
	instVars, botVars, err := b.View()
	if err != nil {
		t.Fatal(err)
	}
	if instVars["REEVE_TEST_GLOBAL"] != "inst" {
		t.Fatalf("global=true 应写实例级：%v", instVars)
	}
	if _, ok := botVars["REEVE_TEST_GLOBAL"]; ok {
		t.Fatalf("global=true 不应写 bot 级：%v", botVars)
	}
	if botVars["REEVE_TEST_LOCAL"] != "bot" {
		t.Fatalf("global=false 应写 bot 级：%v", botVars)
	}
	if _, ok := instVars["REEVE_TEST_LOCAL"]; ok {
		t.Fatalf("global=false 不应写实例级：%v", instVars)
	}
	instanceFile, err := os.ReadFile(filepath.Join(b.cfg.DataDir, "env"))
	if err != nil || !strings.Contains(string(instanceFile), "REEVE_TEST_GLOBAL=inst") {
		t.Fatalf("实例级文件未落盘：%s %v", instanceFile, err)
	}
	botFile, err := os.ReadFile(filepath.Join(b.cfg.DataDir, "env.json"))
	if err != nil || !strings.Contains(string(botFile), "REEVE_TEST_LOCAL") {
		t.Fatalf("bot 级文件未落盘：%s %v", botFile, err)
	}
	raw, err := os.ReadFile(filepath.Join(b.cfg.DataDir, "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"layer":"instance"`) || !strings.Contains(string(raw), `"layer":"bot"`) {
		t.Fatalf("事件应记录所在层: %s", raw)
	}
	if err := b.UnsetEnv("REEVE_TEST_GLOBAL", true); err != nil {
		t.Fatal(err)
	}
	if instVars, _, err = b.View(); err != nil {
		t.Fatal(err)
	}
	if _, ok := instVars["REEVE_TEST_GLOBAL"]; ok {
		t.Fatalf("实例级变量应已删除：%v", instVars)
	}
	raw, err = os.ReadFile(filepath.Join(b.cfg.DataDir, "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"kind":"env.unset"`) || !strings.Contains(string(raw), `"layer":"instance"`) {
		t.Fatal("应写入 layer=instance 的 env.unset 事件")
	}
}

func TestUnsetBotEnvWritesEventWithoutValue(t *testing.T) {
	b := newBot(t, t.TempDir(), nil)
	const value = "http://user:pass@127.0.0.1:7891"
	if err := b.SetEnv("HTTPS_PROXY", value, false); err != nil {
		t.Fatal(err)
	}
	if err := b.UnsetEnv("HTTPS_PROXY", false); err != nil {
		t.Fatal(err)
	}
	got, err := b.BotEnv()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["HTTPS_PROXY"]; ok {
		t.Fatalf("变量应已删除：%v", got)
	}
	raw, err := os.ReadFile(filepath.Join(b.cfg.DataDir, "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "7891") || strings.Contains(string(raw), "pass") {
		t.Fatal("env 事件不应包含值")
	}
	if !strings.Contains(string(raw), `"kind":"env.unset"`) || !strings.Contains(string(raw), "HTTPS_PROXY") {
		t.Fatal("应写入含变量名的 env.unset 事件")
	}
}

// 变量本来就不存在：返回 nil，不重写文件，也不写事件。
func TestUnsetMissingWritesNoEvent(t *testing.T) {
	b := newBot(t, t.TempDir(), nil)
	if err := b.UnsetBotEnv("REEVE_TEST_ABSENT", "user"); err != nil {
		t.Fatal(err)
	}
	if err := b.UnsetInstanceEnv("REEVE_TEST_ABSENT", "user"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(b.cfg.DataDir, "stream.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"kind":"env.unset"`) {
		t.Fatalf("不存在的变量不应写 env.unset 事件：%s", raw)
	}
	for _, name := range []string{"env.json", "env"} {
		if _, err := os.Stat(filepath.Join(b.cfg.DataDir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s 不应因删除不存在的变量而创建：%v", name, err)
		}
	}
}

// 非法变量名/值：返回错误，不落盘，不写事件。
func TestBotEnvRejectsInvalid(t *testing.T) {
	b := newBot(t, t.TempDir(), nil)
	for _, c := range []struct{ name, value string }{
		{"BAD-NAME", "x"},
		{"", "x"},
		{"REEVE_TEST_OK", "line\nbreak"},
		{"REEVE_TEST_OK", strings.Repeat("x", 8193)},
	} {
		if err := b.SetBotEnv(c.name, c.value, "user"); err == nil {
			t.Fatalf("SetBotEnv(%q, %.20q) 应该报错", c.name, c.value)
		}
		if err := b.SetInstanceEnv(c.name, c.value, "user"); err == nil {
			t.Fatalf("SetInstanceEnv(%q, %.20q) 应该报错", c.name, c.value)
		}
	}
	if err := b.UnsetBotEnv("BAD-NAME", "user"); err == nil {
		t.Fatal("UnsetBotEnv 非法变量名应该报错")
	}
	if err := b.UnsetInstanceEnv("BAD-NAME", "user"); err == nil {
		t.Fatal("UnsetInstanceEnv 非法变量名应该报错")
	}
	for _, name := range []string{"env.json", "env"} {
		if _, err := os.Stat(filepath.Join(b.cfg.DataDir, name)); !os.IsNotExist(err) {
			t.Fatalf("非法输入不应落盘 %s：%v", name, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(b.cfg.DataDir, "stream.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"kind":"env.set"`) || strings.Contains(string(raw), `"kind":"env.unset"`) {
		t.Fatalf("非法输入不应写 env 事件：%s", raw)
	}
}

// 命令层、整体替换与单键写入都拒绝 {{secret:…}} 字面量；普通值不受影响。
func TestEnvRejectsSecretRefs(t *testing.T) {
	b := newBot(t, t.TempDir(), nil)
	const hint = "密钥请用设置页的密钥卡片，不要写进环境变量"
	for _, value := range []string{"{{secret:PG_PASS}}", "postgres://user:{{secret:PG_PASS}}@db/app"} {
		if err := b.SetBotEnv("DATABASE_URL", value, "user"); err == nil || !strings.Contains(err.Error(), hint) {
			t.Fatalf("bot 级写入应拒绝 %q，得到 %v", value, err)
		}
		if err := b.SetInstanceEnv("DATABASE_URL", value, "user"); err == nil || !strings.Contains(err.Error(), hint) {
			t.Fatalf("实例级写入应拒绝 %q，得到 %v", value, err)
		}
	}
	if _, err := b.ReplaceBotEnv(map[string]string{"OK_VAR": "fine", "BAD_VAR": "{{secret:X}}"}); err == nil || !strings.Contains(err.Error(), hint) {
		t.Fatalf("整体替换应拒绝密钥引用，得到 %v", err)
	}
	if _, err := b.ReplaceInstanceEnv(map[string]string{"BAD_VAR": "{{secret:X}}"}); err == nil || !strings.Contains(err.Error(), hint) {
		t.Fatalf("实例级整体替换应拒绝密钥引用，得到 %v", err)
	}
	if got, err := b.BotEnv(); err != nil || len(got) != 0 {
		t.Fatalf("被拒绝的整体替换不应落盘任何键：%v %v", got, err)
	}

	res, ok := b.CommandLine(context.Background(), "/env set TOKEN={{secret:PG_PASS}}")
	if !ok || res.OK || !strings.Contains(res.Text, hint) {
		t.Fatalf("/env set 应拒绝密钥引用：%+v", res)
	}
	if got, err := b.BotEnv(); err != nil || len(got) != 0 {
		t.Fatalf("命令层拒绝后不应落盘：%v %v", got, err)
	}
	if err := b.SetEnv("PLAIN_VALUE", "hello", false); err != nil {
		t.Fatalf("普通值不应受影响：%v", err)
	}
}

// 整体替换与单键写入共用 envMu：并发交替执行后必须是某一次完整替换的结果，
// 不能出现旧键复活的混合状态；最后再整体替换一次，结果精确等于 body。
func TestReplaceBotEnvSerializesWithSingleWrites(t *testing.T) {
	b := newBot(t, t.TempDir(), nil)
	for round := 0; round < 20; round++ {
		if err := b.SetBotEnv("REEVE_RACE_OLD", "old", "user"); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			_ = b.SetEnv("REEVE_RACE_SINGLE", strconv.Itoa(i), false)
		}(round)
		go func() {
			defer wg.Done()
			_, _ = b.ReplaceBotEnv(map[string]string{"REEVE_RACE_FINAL": "final"})
		}()
		wg.Wait()
		got, err := b.BotEnv()
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got["REEVE_RACE_OLD"]; ok {
			t.Fatalf("整体替换后旧键复活（读-改-写没有与单键写入串行化）: %v", got)
		}
		for name := range got {
			if name != "REEVE_RACE_FINAL" && name != "REEVE_RACE_SINGLE" {
				t.Fatalf("出现了来路不明的键 %q: %v", name, got)
			}
		}
		if _, err := b.ReplaceBotEnv(map[string]string{"REEVE_RACE_DONE": "1"}); err != nil {
			t.Fatal(err)
		}
		if got, err = b.BotEnv(); err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got["REEVE_RACE_DONE"] != "1" {
			t.Fatalf("最后一次整体替换应精确生效: %v", got)
		}
	}
}
