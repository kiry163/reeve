package bot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kiry163/reeve/internal/builtin"
)

// writeBotSkill 在 dir 下建一个合法的技能目录。
func writeBotSkill(t *testing.T, dir, name, desc, when, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: " + desc + "\nwhen_to_use: " + when + "\n---\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(p, "SKILL.md"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
}

// 放一个技能后，聊天层提示词带索引；没有技能时不带空标题。
func TestChatPromptIncludesSkillIndex(t *testing.T) {
	dir := t.TempDir()
	writeBotSkill(t, filepath.Join(dir, "data", "skills"), "pdf", "处理 PDF", "用户要读 PDF 时", "第一步：先数页数。")
	b := newBot(t, dir, nil)
	defer b.Close()

	prompt := b.chatSystemPrompt()
	for _, want := range []string{"## 技能索引", "pdf — 处理 PDF — 用户要读 PDF 时", "read_skill"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("聊天层提示词缺少 %q：\n%s", want, prompt)
		}
	}

	clean := newBot(t, t.TempDir(), nil)
	defer clean.Close()
	if strings.Contains(clean.chatSystemPrompt(), "## 技能索引") {
		t.Fatalf("没有技能时不应出现索引标题：\n%s", clean.chatSystemPrompt())
	}
}

// 聊天层 read_skill 读正文与相对路径；未知名报错并列出可用名。
func TestChatReadSkill(t *testing.T) {
	dir := t.TempDir()
	writeBotSkill(t, filepath.Join(dir, "data", "skills"), "pdf", "处理 PDF", "用户要读 PDF 时", "第一步：先数页数。")
	writeBotSkill(t, filepath.Join(dir, "data", "skills"), "docx", "处理 Word", "用户要读 Word 时", "先看段落。")
	b := newBot(t, dir, nil)
	defer b.Close()

	tool := chatTool(t, b, "read_skill")
	out, err := tool.Execute(context.Background(), builtin.ToolEnv{}, json.RawMessage(`{"name":"pdf"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"第一步：先数页数。", "（技能文件：skills/pdf/SKILL.md）"} {
		if !strings.Contains(out, want) {
			t.Fatalf("read_skill 输出缺少 %q：\n%s", want, out)
		}
	}

	_, err = tool.Execute(context.Background(), builtin.ToolEnv{}, json.RawMessage(`{"name":"nope"}`))
	if err == nil {
		t.Fatal("未知技能应报错")
	}
	for _, want := range []string{`"nope"`, "pdf", "docx"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误里应包含 %q：%v", want, err)
		}
	}
}

// 技能集合为空时聊天层 read_skill 明确说没有可用技能。
func TestChatReadSkillEmptySet(t *testing.T) {
	b := newBot(t, t.TempDir(), nil)
	defer b.Close()
	_, err := chatTool(t, b, "read_skill").Execute(context.Background(), builtin.ToolEnv{}, json.RawMessage(`{"name":"pdf"}`))
	if err == nil || !strings.Contains(err.Error(), "当前没有可用技能") {
		t.Fatalf("应返回「当前没有可用技能」，得到 %v", err)
	}
}

// ListSkills 返回升序技能名（Task 8 的 BotAPI 依赖它）。
func TestListSkillsAscending(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "data", "skills")
	writeBotSkill(t, root, "zeta", "Z", "需要 Z 时", "Z 的步骤。")
	writeBotSkill(t, root, "alpha", "A", "需要 A 时", "A 的步骤。")
	b := newBot(t, dir, nil)
	defer b.Close()
	if got, want := b.ListSkills(), []string{"alpha", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ListSkills() = %v, want %v", got, want)
	}
}

// 主 Bot 的实例目录就是数据目录：同一个技能根只扫一次，坏文件只报一条错误。
func TestMainBotScansSkillsOnce(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "data", "skills", "bad")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: bad\ndescription: 坏的技能\n---\n\n步骤。\n" // 缺 when_to_use
	if err := os.WriteFile(filepath.Join(bad, "SKILL.md"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	b := newBot(t, dir, nil)
	defer b.Close()
	if got := len(b.skills.Errors()); got != 1 {
		t.Fatalf("同一个坏技能应只报一条错误，得到 %d 条：%v", got, b.skills.Errors())
	}
}

func TestListSkillsEmpty(t *testing.T) {
	b := newBot(t, t.TempDir(), nil)
	defer b.Close()
	if got := b.ListSkills(); len(got) != 0 {
		t.Fatalf("没有技能时 ListSkills() = %v", got)
	}
}
