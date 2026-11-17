package bot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kiry163/reeve/internal/builtin"
)

// 长时间没有任何事件：监工请求在安全点暂停（不是取消），任务变 paused(stall) 且检查点保留。
func TestStallPausesWithCheckpoint(t *testing.T) {
	dir := t.TempDir()
	ai := &fakeAI{agent: func(ctx context.Context, ms []builtin.Message) (builtin.Message, error) {
		if toolSteps(ms) == 0 {
			return toolCall("write_file", map[string]string{"path": "a.txt", "content": "one"}), nil
		}
		<-ctx.Done() // 模型卡住，不再产生任何事件
		return builtin.Message{}, ctx.Err()
	}}
	b := newBotWith(t, dir, ai, func(c *Config) { c.StallTimeout = 120 * time.Millisecond })
	b.Start()
	defer b.Close()
	id, _ := b.StartTask("写文件后卡住", "卡住")
	waitFor(t, "paused by stall", func() bool {
		tk, _ := b.store.Task(id)
		return tk.Status == "paused" && tk.StopReason == "stall"
	})
	if _, ok := b.loadCheckpoint(id); !ok {
		t.Fatal("stall pause must keep the checkpoint")
	}
	tk, _ := b.store.Task(id)
	note := strings.Join(tk.Notes, "\n")
	if !strings.Contains(note, "没有任何进展") {
		t.Fatalf("stall should leave a note, got %q", note)
	}
}

// 模型持续输出思考片段时不算卡死。
func TestThinkingKeepsTaskAlive(t *testing.T) {
	dir := t.TempDir()
	ai := &fakeAI{agentStream: func(ctx context.Context, reasoning func(string)) (builtin.Message, error) {
		for range 12 {
			select {
			case <-ctx.Done():
				return builtin.Message{}, ctx.Err()
			case <-time.After(20 * time.Millisecond):
			}
			if reasoning != nil {
				reasoning("继续想一想，")
			}
		}
		return builtin.Message{Content: "想好了"}, nil
	}}
	b := newBotWith(t, dir, ai, func(c *Config) { c.StallTimeout = 100 * time.Millisecond })
	b.Start()
	defer b.Close()
	id, _ := b.StartTask("长时间思考", "思考")
	waitFor(t, "completed", func() bool { tk, _ := b.store.Task(id); return tk.Status == "completed" })
	tk, _ := b.store.Task(id)
	if tk.StopReason == "stall" || len(tk.Notes) > 0 {
		t.Fatalf("thinking task should not be treated as stalled: %+v", tk)
	}
}

// 失败（非用户取消）时保留检查点，方便排查/续跑。
func TestCheckpointKeptOnFailure(t *testing.T) {
	dir := t.TempDir()
	ai := &fakeAI{agent: func(_ context.Context, ms []builtin.Message) (builtin.Message, error) {
		if toolSteps(ms) == 0 {
			return toolCall("write_file", map[string]string{"path": "a.txt", "content": "one"}), nil
		}
		return builtin.Message{}, errors.New("模型服务挂了")
	}}
	b := newBot(t, dir, ai)
	b.Start()
	defer b.Close()
	id, _ := b.StartTask("写一步然后失败", "失败")
	waitFor(t, "failed", func() bool { tk, _ := b.store.Task(id); return tk.Status == "failed" })
	if _, ok := b.loadCheckpoint(id); !ok {
		t.Fatal("failed task must keep its checkpoint")
	}
}
