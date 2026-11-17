package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiry163/reeve/internal/skills"
)

// writeSkill 在 root 下建一个合法的技能目录。
func writeSkill(t *testing.T, root, name, desc, when, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: " + desc + "\nwhen_to_use: " + when + "\n---\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
}

// 执行器的系统提示按 Task.Skills 注入索引：标题、三列索引与 read_skill 指引都在。
func TestTaskSystemPromptIncludesSkillIndex(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "pdf", "处理 PDF", "用户要读 PDF 时", "第一步：先数页数。")
	a := &Agent{}
	msgs, err := a.initialMessages(context.Background(), Task{ID: "t", Workspace: t.TempDir(), Input: "hi", Skills: skills.Load(root, "")})
	if err != nil {
		t.Fatal(err)
	}
	sys := msgs[0].Content
	for _, want := range []string{"## 技能索引", "pdf — 处理 PDF — 用户要读 PDF 时", "read_skill"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("系统提示缺少 %q：\n%s", want, sys)
		}
	}
}

// 没有技能时不注入空标题；Resume 路径也要带上索引（系统提示只组装一次）。
func TestTaskSystemPromptWithoutSkillsHasNoHeading(t *testing.T) {
	a := &Agent{}
	for _, task := range []Task{
		{ID: "t", Workspace: t.TempDir(), Input: "hi"},
		{ID: "t", Workspace: t.TempDir(), Input: "hi", Skills: skills.Load("", "")},
	} {
		msgs, err := a.initialMessages(context.Background(), task)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(msgs[0].Content, "## 技能索引") {
			t.Fatalf("没有技能时不应出现索引标题：\n%s", msgs[0].Content)
		}
	}
}

// read_skill 返回正文和相对文件路径；两层的格式化共用一个实现。
func TestReadSkillReturnsBodyAndRelativePath(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "pdf", "处理 PDF", "用户要读 PDF 时", "第一步：先数页数。")
	set := skills.Load(root, "")
	out, err := readSkillTool.Execute(context.Background(), ToolEnv{Skills: set}, json.RawMessage(`{"name":"pdf"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"第一步：先数页数。", "（技能文件：skills/pdf/SKILL.md）"} {
		if !strings.Contains(out, want) {
			t.Fatalf("read_skill 输出缺少 %q：\n%s", want, out)
		}
	}
	if strings.Contains(out, root) {
		t.Fatalf("不应泄漏绝对路径 %s：\n%s", root, out)
	}
}

// 名字不对时报错并列出可用技能，让模型能改口。
func TestReadSkillUnknownNameListsAvailable(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "pdf", "处理 PDF", "用户要读 PDF 时", "看页数。")
	writeSkill(t, root, "docx", "处理 Word", "用户要读 Word 时", "看段落。")
	_, err := ReadSkill(skills.Load(root, ""), "nope")
	if err == nil {
		t.Fatal("未知技能应报错")
	}
	for _, want := range []string{`"nope"`, "pdf", "docx"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误里应包含 %q：%v", want, err)
		}
	}
}

// 集合为 nil 或空时明确说「当前没有可用技能」。
func TestReadSkillEmptySet(t *testing.T) {
	for _, set := range []*skills.Set{nil, skills.Load("", "")} {
		if _, err := ReadSkill(set, "pdf"); err == nil || err.Error() != "当前没有可用技能" {
			t.Fatalf("空集合应返回「当前没有可用技能」，得到 %v", err)
		}
	}
	if _, err := readSkillTool.Execute(context.Background(), ToolEnv{}, json.RawMessage(`{"name":"pdf"}`)); err == nil ||
		!strings.Contains(err.Error(), "当前没有可用技能") {
		t.Fatalf("工具层也要返回「当前没有可用技能」，得到 %v", err)
	}
}

// read_skill 是只读工具，并注册在默认工具集里。
func TestReadSkillToolRegisteredReadOnly(t *testing.T) {
	var found Tool
	for _, tl := range DefaultTools() {
		if tl.Name() == "read_skill" {
			found = tl
		}
	}
	if found == nil {
		t.Fatal("默认工具集里应有 read_skill")
	}
	if got := found.Risk(json.RawMessage(`{"name":"pdf"}`)); got != RiskRead {
		t.Fatalf("read_skill 应为只读，得到 %v", got)
	}
}
