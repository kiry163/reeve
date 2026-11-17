// Package skills 加载本地 SKILL.md 技能：扫描目录、校验 frontmatter、生成索引。
//
// 技能放在 <root>/<name>/SKILL.md。全局根与 bot 根各扫描一层，bot 根里的同名
// 技能覆盖全局。SKILL.md 以 `---` 包裹的 frontmatter 开头，必填 name、
// description、when_to_use 三个字段；其余内容为正文，不能为空。
//
// 单个技能解析失败只记录到 Set.Errors 并跳过，不影响其他技能，也不影响启动。
// 目录不存在视为空集合。本包只依赖标准库，frontmatter 用最小手工解析，
// 不引入 YAML 依赖。
package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Skill 是一个加载成功的技能。
type Skill struct {
	Name        string
	Description string
	WhenToUse   string
	Body        string
	Path        string
}

// Set 是某个加载时刻的技能集合：按名字索引，列表按名字升序，另外保存
// 加载过程中被跳过的技能的错误。
type Set struct {
	skills []Skill
	byName map[string]Skill
	errs   []error
}

// nameRe 约束技能名：小写字母或数字开头，只含小写字母、数字和连字符，
// 长度 2 到 64 个字符。
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)

// Load 扫描全局根与 bot 根下的技能。bot 根中的同名技能覆盖全局。
// 两个根都可以为空或不存在：那表示该层没有技能，不是错误。
func Load(globalDir, botDir string) *Set {
	s := &Set{byName: make(map[string]Skill)}
	s.loadDir(globalDir, "全局技能")
	s.loadDir(botDir, "bot 技能")
	s.skills = make([]Skill, 0, len(s.byName))
	for _, sk := range s.byName {
		s.skills = append(s.skills, sk)
	}
	sort.Slice(s.skills, func(i, j int) bool { return s.skills[i].Name < s.skills[j].Name })
	return s
}

// loadDir 扫描一个技能根目录。同一层内重名视为错误；跨层由调用顺序决定覆盖。
func (s *Set) loadDir(dir, label string) {
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.errs = append(s.errs, fmt.Errorf("读取技能目录 %s 失败: %w，检查这个路径是否可读", dir, err))
		}
		return
	}
	seen := make(map[string]string)
	for _, entry := range entries {
		// 用 Stat 而不是 DirEntry.IsDir，让符号链接的技能目录也能加载。
		info, err := os.Stat(filepath.Join(dir, entry.Name()))
		if err != nil || !info.IsDir() {
			continue
		}
		rel := filepath.Join(entry.Name(), "SKILL.md")
		path := filepath.Join(dir, entry.Name(), "SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil {
			// 目录存在但没有 SKILL.md 不算技能，也不报错。
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			s.errs = append(s.errs, fmt.Errorf("%s %s 读取失败: %w，检查这个文件是否可读", label, rel, err))
			continue
		}
		sk, err := parseSkill(path, data)
		if err != nil {
			s.errs = append(s.errs, fmt.Errorf("%s %s: %w", label, rel, err))
			continue
		}
		if prev, ok := seen[sk.Name]; ok {
			s.errs = append(s.errs, fmt.Errorf("%s %s: 技能名 %q 与 %s 重复，跳过这个", label, rel, sk.Name, prev))
			continue
		}
		seen[sk.Name] = rel
		s.byName[sk.Name] = sk
	}
}

// parseSkill 解析 SKILL.md 内容：首行 `---`，到下一个 `---` 为 frontmatter，
// 逐行 key: value；无法解析成键值的行视为未知键的值并忽略。剩余内容为正文。
// 容忍 UTF-8 BOM 与 CRLF。
func parseSkill(path string, data []byte) (Skill, error) {
	// 先剥 UTF-8 BOM 再归一化换行：Windows 编辑器保存的文件首行可能带 BOM。
	text := strings.TrimPrefix(string(data), "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return Skill{}, fmt.Errorf("缺少 frontmatter：第一行必须是 ---")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return Skill{}, fmt.Errorf("frontmatter 没有闭合：补一行 ---")
	}

	var sk Skill
	for i := 1; i < end; i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			// 未知键可能是 YAML 块列表的续行（如 "- bash"），它的值与本包无关。
			continue
		}
		switch strings.TrimSpace(key) {
		case "name":
			sk.Name = strings.TrimSpace(value)
		case "description":
			sk.Description = strings.TrimSpace(value)
		case "when_to_use":
			sk.WhenToUse = strings.TrimSpace(value)
		}
	}

	if sk.Name == "" {
		return Skill{}, fmt.Errorf("缺少必填字段 name")
	}
	if !nameRe.MatchString(sk.Name) {
		return Skill{}, fmt.Errorf("技能名 %q 不合法：只能用小写字母、数字和连字符，2-64 个字符，且以字母或数字开头（例如 pdf-tools）", sk.Name)
	}
	if sk.Description == "" {
		return Skill{}, fmt.Errorf("缺少必填字段 description")
	}
	if sk.WhenToUse == "" {
		return Skill{}, fmt.Errorf("缺少必填字段 when_to_use")
	}

	sk.Body = strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	if sk.Body == "" {
		return Skill{}, fmt.Errorf("正文为空：技能至少要写操作步骤")
	}
	sk.Path = path
	return sk, nil
}

// List 返回按名字升序排列的技能副本，调用方修改结果不影响集合。
func (s *Set) List() []Skill {
	return append([]Skill(nil), s.skills...)
}

// Get 按名字查找技能。
func (s *Set) Get(name string) (Skill, bool) {
	sk, ok := s.byName[name]
	return sk, ok
}

// Errors 返回加载过程中被跳过的技能的错误副本。
func (s *Set) Errors() []error {
	return append([]error(nil), s.errs...)
}

// Index 生成注入系统提示的索引：每个技能一行
// "name — description — when_to_use"，顺序与 List 一致。没有技能时返回空串。
func (s *Set) Index() string {
	var b strings.Builder
	for i, sk := range s.skills {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s — %s — %s", sk.Name, sk.Description, sk.WhenToUse)
	}
	return b.String()
}
