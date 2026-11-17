package skills

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeSkill 在 root/<dir>/SKILL.md 写入内容，返回文件路径。
func writeSkill(t *testing.T, root, dir, content string) string {
	t.Helper()
	path := filepath.Join(root, dir, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建技能目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入技能文件失败: %v", err)
	}
	return path
}

func TestLoadValidSkill(t *testing.T) {
	dir := t.TempDir()
	path := writeSkill(t, dir, "pdf", "---\r\nname: pdf\r\ndescription: 处理 PDF 文档\r\nwhen_to_use: 用户要求读取或拆分 PDF 时\r\nallowed-tools: bash\r\n---\r\n\r\n1. 打开文件\n2. 读取页面\n")

	set := Load(dir, "")
	if errs := set.Errors(); len(errs) != 0 {
		t.Fatalf("不应有错误，得到 %v", errs)
	}
	list := set.List()
	if len(list) != 1 {
		t.Fatalf("技能数量 = %d，期望 1", len(list))
	}
	got := list[0]
	if got.Name != "pdf" {
		t.Errorf("Name = %q，期望 pdf", got.Name)
	}
	if got.Description != "处理 PDF 文档" {
		t.Errorf("Description = %q", got.Description)
	}
	if got.WhenToUse != "用户要求读取或拆分 PDF 时" {
		t.Errorf("WhenToUse = %q", got.WhenToUse)
	}
	if got.Body != "1. 打开文件\n2. 读取页面" {
		t.Errorf("Body = %q", got.Body)
	}
	if got.Path != path {
		t.Errorf("Path = %q，期望 %q", got.Path, path)
	}
	if _, ok := set.Get("pdf"); !ok {
		t.Error("Get(pdf) 没有找到技能")
	}
	want := "pdf — 处理 PDF 文档 — 用户要求读取或拆分 PDF 时"
	if idx := set.Index(); idx != want {
		t.Errorf("Index() = %q，期望 %q", idx, want)
	}
}

func TestMissingWhenToUseIsSkipped(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "broken", "---\nname: broken\ndescription: 缺字段\n---\n正文\n")
	writeSkill(t, dir, "good", "---\nname: good\ndescription: 正常\nwhen_to_use: 总是\n---\n正文\n")

	set := Load(dir, "")
	if _, ok := set.Get("broken"); ok {
		t.Error("缺 when_to_use 的技能不应进入集合")
	}
	if _, ok := set.Get("good"); !ok {
		t.Error("其他合法技能应继续加载")
	}
	errs := set.Errors()
	if len(errs) != 1 {
		t.Fatalf("错误数量 = %d，期望 1: %v", len(errs), errs)
	}
	msg := errs[0].Error()
	if !strings.Contains(msg, "broken/SKILL.md") {
		t.Errorf("错误应包含相对路径，得到 %q", msg)
	}
	if !strings.Contains(msg, "when_to_use") {
		t.Errorf("错误应说明缺少 when_to_use，得到 %q", msg)
	}
}

func TestBotOverridesGlobal(t *testing.T) {
	globalDir := t.TempDir()
	botDir := t.TempDir()
	writeSkill(t, globalDir, "pdf", "---\nname: pdf\ndescription: 全局版本\nwhen_to_use: 全局时机\n---\n全局正文\n")
	botPath := writeSkill(t, botDir, "pdf", "---\nname: pdf\ndescription: bot 版本\nwhen_to_use: bot 时机\n---\nbot 正文\n")

	set := Load(globalDir, botDir)
	if errs := set.Errors(); len(errs) != 0 {
		t.Fatalf("不应有错误，得到 %v", errs)
	}
	if n := len(set.List()); n != 1 {
		t.Fatalf("技能数量 = %d，期望 1", n)
	}
	sk, ok := set.Get("pdf")
	if !ok {
		t.Fatal("Get(pdf) 没有找到技能")
	}
	if sk.Description != "bot 版本" || sk.WhenToUse != "bot 时机" || sk.Body != "bot 正文" {
		t.Errorf("bot 同名技能没有覆盖全局: %+v", sk)
	}
	if sk.Path != botPath {
		t.Errorf("Path = %q，期望 %q", sk.Path, botPath)
	}
}

func TestInvalidNameRejected(t *testing.T) {
	tests := []struct {
		name string
		dir  string
	}{
		{"大写", "Upper"},
		{"下划线", "bad_name"},
		{"过长", strings.Repeat("a", 65)},
		{"单字符", "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSkill(t, dir, tt.dir, "---\nname: "+tt.dir+"\ndescription: 描述\nwhen_to_use: 时机\n---\n正文\n")
			set := Load(dir, "")
			if len(set.List()) != 0 {
				t.Errorf("非法名字 %q 不应加载: %+v", tt.dir, set.List())
			}
			errs := set.Errors()
			if len(errs) != 1 {
				t.Fatalf("错误数量 = %d，期望 1: %v", len(errs), errs)
			}
			if !strings.Contains(errs[0].Error(), "不合法") {
				t.Errorf("错误应说明名字不合法，得到 %q", errs[0].Error())
			}
		})
	}
}

func TestEmptyBodyRejected(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "empty", "---\nname: empty\ndescription: 空正文\nwhen_to_use: 时机\n---\n\n   \n")

	set := Load(dir, "")
	if _, ok := set.Get("empty"); ok {
		t.Error("正文为空的技能不应进入集合")
	}
	errs := set.Errors()
	if len(errs) != 1 {
		t.Fatalf("错误数量 = %d，期望 1: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), "正文为空") {
		t.Errorf("错误应说明正文为空，得到 %q", errs[0].Error())
	}
}

func TestCRLFParsed(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "crlf", "---\r\nname: crlf\r\ndescription: CRLF\r\nwhen_to_use: CRLF 文件\r\n---\r\n步骤一\r\n步骤二\r\n")

	set := Load(dir, "")
	if errs := set.Errors(); len(errs) != 0 {
		t.Fatalf("CRLF 文件不应报错，得到 %v", errs)
	}
	sk, ok := set.Get("crlf")
	if !ok {
		t.Fatal("CRLF 技能没有加载")
	}
	if sk.Description != "CRLF" {
		t.Errorf("Description = %q", sk.Description)
	}
	if sk.Body != "步骤一\n步骤二" {
		t.Errorf("Body = %q，期望去 CRLF 后的正文", sk.Body)
	}
}

func TestMissingDirectories(t *testing.T) {
	base := t.TempDir()
	set := Load(filepath.Join(base, "no-global"), filepath.Join(base, "no-bot"))
	if n := len(set.List()); n != 0 {
		t.Errorf("技能数量 = %d，期望 0", n)
	}
	if errs := set.Errors(); len(errs) != 0 {
		t.Errorf("目录不存在不应报错，得到 %v", errs)
	}
	if idx := set.Index(); idx != "" {
		t.Errorf("空集合 Index() = %q，期望空串", idx)
	}

	empty := Load("", "")
	if n := len(empty.List()); n != 0 {
		t.Errorf("空路径技能数量 = %d，期望 0", n)
	}
	if errs := empty.Errors(); len(errs) != 0 {
		t.Errorf("空路径不应报错，得到 %v", errs)
	}
}

func TestIndexOrderAndFormat(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "zeta", "---\nname: zeta\ndescription: Z 描述\nwhen_to_use: Z 时机\n---\n正文\n")
	writeSkill(t, dir, "alpha", "---\nname: alpha\ndescription: A 描述\nwhen_to_use: A 时机\n---\n正文\n")

	set := Load(dir, "")
	want := "alpha — A 描述 — A 时机\nzeta — Z 描述 — Z 时机"
	if idx := set.Index(); idx != want {
		t.Errorf("Index() = %q，期望 %q", idx, want)
	}
	list := set.List()
	if len(list) != 2 || list[0].Name != "alpha" || list[1].Name != "zeta" {
		t.Errorf("List() 未按名字升序: %+v", list)
	}
}

func TestBOMHandled(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "bom", "\ufeff---\nname: bom\ndescription: 带 BOM\nwhen_to_use: BOM 文件\n---\n正文\n")

	set := Load(dir, "")
	if errs := set.Errors(); len(errs) != 0 {
		t.Fatalf("BOM 文件不应报错，得到 %v", errs)
	}
	sk, ok := set.Get("bom")
	if !ok {
		t.Fatal("带 BOM 的技能没有加载")
	}
	if sk.Description != "带 BOM" || sk.Body != "正文" {
		t.Errorf("BOM 技能字段不对: %+v", sk)
	}
}

func TestBlockListKeyIgnored(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "block", "---\nname: block\ndescription: 含块列表\nwhen_to_use: 时机\nallowed-tools:\n  - bash\n  - read\ntags:\n  - a\n---\n正文\n")
	writeSkill(t, dir, "broken", "---\nname: broken\ndescription: 缺字段但有列表\nallowed-tools:\n  - bash\n---\n正文\n")

	set := Load(dir, "")
	if _, ok := set.Get("block"); !ok {
		t.Error("含未知块列表键的合法技能应正常加载")
	}
	if _, ok := set.Get("broken"); ok {
		t.Error("缺 when_to_use 的技能不应加载")
	}
	errs := set.Errors()
	if len(errs) != 1 {
		t.Fatalf("错误数量 = %d，期望 1: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), "when_to_use") {
		t.Errorf("错误应说明缺少 when_to_use，得到 %q", errs[0].Error())
	}
}

func TestSymlinkSkillDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上创建符号链接需要额外权限")
	}
	target := t.TempDir()
	writeSkill(t, target, "pdf", "---\nname: pdf\ndescription: 链接技能\nwhen_to_use: 时机\n---\n正文\n")

	root := t.TempDir()
	if err := os.Symlink(filepath.Join(target, "pdf"), filepath.Join(root, "pdf")); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}

	set := Load(root, "")
	if errs := set.Errors(); len(errs) != 0 {
		t.Fatalf("符号链接目录不应报错，得到 %v", errs)
	}
	sk, ok := set.Get("pdf")
	if !ok {
		t.Fatal("符号链接指向的技能目录没有加载")
	}
	if want := filepath.Join(root, "pdf", "SKILL.md"); sk.Path != want {
		t.Errorf("Path = %q，期望 %q", sk.Path, want)
	}
}

func TestUnclosedFrontmatterRejected(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "open", "---\nname: open\ndescription: 未闭合\nwhen_to_use: 时机\n正文\n")

	set := Load(dir, "")
	if len(set.List()) != 0 {
		t.Errorf("frontmatter 未闭合不应加载: %+v", set.List())
	}
	errs := set.Errors()
	if len(errs) != 1 {
		t.Fatalf("错误数量 = %d，期望 1: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), "闭合") {
		t.Errorf("错误应说明 frontmatter 未闭合，得到 %q", errs[0].Error())
	}
}

func TestDuplicateNameSameLayer(t *testing.T) {
	dir := t.TempDir()
	firstPath := writeSkill(t, dir, "a", "---\nname: dup\ndescription: a 版本\nwhen_to_use: 时机\n---\n正文\n")
	writeSkill(t, dir, "b", "---\nname: dup\ndescription: b 版本\nwhen_to_use: 时机\n---\n正文\n")

	set := Load(dir, "")
	list := set.List()
	if len(list) != 1 {
		t.Fatalf("同层重名应只保留一个，得到 %+v", list)
	}
	// ReadDir 按目录名排序，a 先出现，因此 a 保留。
	if list[0].Path != firstPath || list[0].Description != "a 版本" {
		t.Errorf("保留的技能 = %+v，期望 a 版本", list[0])
	}
	errs := set.Errors()
	if len(errs) != 1 {
		t.Fatalf("错误数量 = %d，期望 1: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), "重复") {
		t.Errorf("错误应说明名字重复，得到 %q", errs[0].Error())
	}
}

func TestListReturnsCopy(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "pdf", "---\nname: pdf\ndescription: 原始\nwhen_to_use: 时机\n---\n正文\n")
	set := Load(dir, "")

	list := set.List()
	if len(list) != 1 {
		t.Fatalf("技能数量 = %d，期望 1", len(list))
	}
	list[0].Name = "changed"
	list[0].Description = "被改"
	list[0].Body = "被改"
	list = append(list, Skill{Name: "extra"})

	sk, ok := set.Get("pdf")
	if !ok {
		t.Fatal("修改返回值不应影响内部集合")
	}
	if sk.Description != "原始" || sk.Body != "正文" {
		t.Errorf("内部技能被返回值修改: %+v", sk)
	}
	again := set.List()
	if len(again) != 1 || again[0].Name != "pdf" {
		t.Errorf("再次 List() = %+v，期望仍只有 pdf", again)
	}

	errs := set.Errors()
	if len(errs) != 0 {
		t.Fatalf("不应有错误，得到 %v", errs)
	}
}
