package bot

import (
	"log"
	"path/filepath"
	"strings"

	"github.com/kiry163/reeve/internal/builtin"
	"github.com/kiry163/reeve/internal/skills"
)

// loadSkills 在启动时加载全局技能与 bot 私有技能（bot 同名技能覆盖全局）。
// 只在启动时加载一次，不支持运行时重载。
func (b *Bot) loadSkills() {
	globalDir := filepath.Join(b.instanceEnvDir(), "skills")
	botDir := filepath.Join(b.cfg.DataDir, "skills")
	if filepath.Clean(globalDir) == filepath.Clean(botDir) {
		// 主 Bot 的实例目录就是数据目录：同一个根只扫一次，避免重复读文件与重复报错。
		botDir = ""
	}
	b.skills = skills.Load(globalDir, botDir)
	for _, err := range b.skills.Errors() {
		log.Printf("技能加载失败: %v", err)
	}
}

// ListSkills 返回可用技能名（升序），供命令面板与技能索引接口使用。
func (b *Bot) ListSkills() []string {
	if b.skills == nil {
		return nil
	}
	names := make([]string, 0, len(b.skills.List()))
	for _, sk := range b.skills.List() {
		names = append(names, sk.Name)
	}
	return names
}

// skillInfo 是 GET /api/skills 里的一个技能。Path 是相对标签（skills/<目录>/SKILL.md），
// 不暴露实例数据目录或 home 的绝对路径。
type skillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	WhenToUse   string `json:"when_to_use"`
	Path        string `json:"path"`
}

// skillIndexView 是 GET /api/skills 的响应；两个字段都初始化为空切片，序列化成 [] 而不是 null。
type skillIndexView struct {
	Skills []skillInfo `json:"skills"`
	Errors []string    `json:"errors"`
}

// SkillIndex 返回启动时加载的技能索引与被跳过的技能错误，供 GET /api/skills 使用。
// 集合只在启动时加载一次，这里不触发重扫。
func (b *Bot) SkillIndex() skillIndexView {
	out := skillIndexView{Skills: []skillInfo{}, Errors: []string{}}
	if b.skills == nil {
		return out
	}
	for _, sk := range b.skills.List() {
		out.Skills = append(out.Skills, skillInfo{Name: sk.Name, Description: sk.Description,
			WhenToUse: sk.WhenToUse, Path: builtin.SkillFileLabel(sk.Path)})
	}
	for _, err := range b.skills.Errors() {
		out.Errors = append(out.Errors, b.redactDataDir(err.Error()))
	}
	return out
}

// redactDataDir 把技能 / 环境变量错误里的绝对路径换成相对标签。错误文案由
// skills、envstore 包生成，可能内嵌数据目录，或包住 os.ReadFile 的
// "open /abs/...: permission denied"；命令结果、工具输出与 HTTP 响应里都不能
// 出现绝对 home 路径。前缀按从长到短替换，避免子路径被父路径截断。
func (b *Bot) redactDataDir(msg string) string {
	prefixes := []struct{ abs, label string }{
		{filepath.Join(b.cfg.DataDir, "skills"), "skills"},      // bot 私有技能根
		{filepath.Join(b.instanceEnvDir(), "skills"), "skills"}, // 实例级技能根（主 Bot 与上一行相同）
		{b.cfg.DataDir, "data"},                                 // bot 数据目录（env.json 等）
		{b.instanceEnvDir(), "data"},                            // 实例目录（env 等；非默认 Bot 指向主数据目录）
	}
	for _, p := range prefixes {
		if p.abs == "" || p.abs == "." {
			continue
		}
		// 先按「目录 + 分隔符」替换，避免 /a/b 命中 /a/bc 这种同前缀的兄弟目录。
		msg = strings.ReplaceAll(msg, p.abs+string(filepath.Separator), p.label+string(filepath.Separator))
		msg = strings.ReplaceAll(msg, p.abs, p.label)
	}
	return msg
}
