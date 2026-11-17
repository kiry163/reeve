package bot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiry163/reeve/internal/builtin"
)

// 任务启动时的执行层环境包含 bot 级变量（实例级/bot 级/base 由 TaskEnv 合并）。
func TestCreateTaskEnvListIncludesBotEnv(t *testing.T) {
	b := newBot(t, t.TempDir(), nil)
	if err := b.SetBotEnv("REEVE_TEST_TASK_ENV", "from-bot", "user"); err != nil {
		t.Fatal(err)
	}
	id, err := b.createTask("做点什么", "测试任务", TaskOpts{Hold: true}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := b.store.Task(id)
	if !ok {
		t.Fatalf("任务 %s 未落库", id)
	}
	task := b.builtinTask(rec, b.botProfile())
	if task.EnvList == nil {
		t.Fatal("EnvList 为 nil，执行器会回退继承进程环境，丢失 bot 级变量")
	}
	if joined := strings.Join(task.EnvList, "\n"); !strings.Contains(joined, "REEVE_TEST_TASK_ENV=from-bot") {
		t.Fatalf("任务环境缺少 bot 级变量\n%s", joined)
	}
}

// bot 级变量的值只进执行层环境，不能写进事件日志（stream.jsonl）。
func TestTaskEnvValuesNotPersisted(t *testing.T) {
	const sentinel = "do-not-log-9f3a"
	ai := &fakeAI{agent: func(_ context.Context, ms []builtin.Message) (builtin.Message, error) {
		if toolSteps(ms) == 0 {
			return toolCall("shell", map[string]string{"command": "echo hello"}), nil
		}
		return builtin.Message{Content: "完成"}, nil
	}}
	dir := t.TempDir()
	b := newBot(t, dir, ai)
	if err := b.SetBotEnv("REEVE_SENTINEL", sentinel, "user"); err != nil {
		t.Fatal(err)
	}
	b.Start()
	defer b.Close()
	id, err := b.StartTask("跑一下", "测试任务")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "task completed", func() bool {
		rec, ok := b.store.Task(id)
		return ok && rec.Status == "completed"
	})
	raw, err := os.ReadFile(filepath.Join(dir, "data", "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), sentinel) {
		t.Fatal("bot 级环境变量的值不能落进 stream.jsonl")
	}
	if !strings.Contains(string(raw), `"kind":"env.set"`) || !strings.Contains(string(raw), "REEVE_SENTINEL") {
		t.Fatal("应写入含变量名、不含值的 env.set 事件")
	}
}
