package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/kiry163/reeve/internal/skills"
)

// read_skill 的元数据只在这里维护一份：聊天层与执行器各有一份工具注册表
// （两层的工具集不同），但名字、描述与 schema 都从这里取，避免两份定义漂移。
const (
	ReadSkillName   = "read_skill"
	ReadSkillDesc   = "读取一个技能的完整正文（本地 SKILL.md）。先看系统提示里的技能索引，需要时按名字读取。"
	ReadSkillSchema = `{"type":"object","properties":{"name":{"type":"string","description":"技能名，取自技能索引"}},"required":["name"]}`
)

// readSkillTool 按名字读取技能正文。系统提示里只有索引，
// 正文按需读取，避免把每个技能都塞进上下文。
var readSkillTool = FuncTool{ReadSkillName, ReadSkillDesc, ReadSkillSchema,
	readOnly, func(ctx context.Context, env ToolEnv, raw json.RawMessage) (string, error) {
		a, err := parseArgs[struct{ Name string }](raw)
		if err != nil {
			return "", err
		}
		return ReadSkill(env.Skills, a.Name)
	}}

// SkillsSection 生成注入系统提示的技能索引段：标题、每行一个技能、一句使用说明。
// 集合为空（或 nil）时返回空串，调用方据此完全不追加，避免留下空的标题。
// 聊天层与后台执行器共用这一段，措辞保持一致。
func SkillsSection(set *skills.Set) string {
	if set == nil {
		return ""
	}
	index := set.Index()
	if index == "" {
		return ""
	}
	return "## 技能索引\n" + index + "\n\n需要时用 read_skill 读取正文，不要在回答里复述整篇技能。"
}

// ReadSkill 返回技能正文与文件位置，供内置工具与聊天层工具共用，保证两处格式一致。
// 没有技能时明确说「当前没有可用技能」；名字不对时报错并列出可用名字，方便模型改口。
func ReadSkill(set *skills.Set, name string) (string, error) {
	index := ""
	if set != nil {
		index = set.Index()
	}
	if index == "" {
		return "", errors.New("当前没有可用技能")
	}
	sk, ok := set.Get(strings.TrimSpace(name))
	if !ok {
		names := make([]string, 0, len(set.List()))
		for _, s := range set.List() {
			names = append(names, s.Name)
		}
		return "", fmt.Errorf("没有技能 %q；可用技能：%s", strings.TrimSpace(name), strings.Join(names, "、"))
	}
	return sk.Body + "\n\n（技能文件：" + SkillFileLabel(sk.Path) + "）", nil
}

// SkillFileLabel 把技能文件路径显示成相对数据目录的 skills/<目录>/SKILL.md，
// 不把实例数据目录或 home 的绝对路径带进模型上下文与 HTTP 响应。
// 执行器的 read_skill 与聊天层的技能索引共用这一份格式化。
func SkillFileLabel(p string) string {
	return filepath.Join("skills", filepath.Base(filepath.Dir(p)), filepath.Base(p))
}
