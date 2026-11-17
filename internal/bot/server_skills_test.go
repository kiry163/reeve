package bot

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type skillsPayload struct {
	Skills []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		WhenToUse   string `json:"when_to_use"`
		Path        string `json:"path"`
	} `json:"skills"`
	Errors []string `json:"errors"`
}

// GET /api/bots/{id}/skills：一个合法技能 + 一个缺必填字段的技能 → 1 条技能 + 1 条错误；
// path 是 skills/<目录>/SKILL.md 相对标签，不带 home 绝对路径。
func TestSkillsIndex(t *testing.T) {
	dir := t.TempDir()
	writeBotSkill(t, filepath.Join(dir, "data", "skills"), "pdf", "处理 PDF", "用户要读 PDF 时", "第一步：先数页数。")
	broken := filepath.Join(dir, "data", "skills", "broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: broken\ndescription: 缺 when_to_use\n---\n\n正文\n"
	if err := os.WriteFile(filepath.Join(broken, "SKILL.md"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}

	bs, err := OpenBots(Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bs.Close)

	w, body := envRequest(t, bs.Handler(nil), "GET", "/api/bots/main/skills", "")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200：%s", w.Code, body)
	}
	var payload skillsPayload
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("响应不是技能索引 JSON: %v: %s", err, body)
	}
	if len(payload.Skills) != 1 {
		t.Fatalf("技能数 = %d，期望 1：%s", len(payload.Skills), body)
	}
	sk := payload.Skills[0]
	if sk.Name != "pdf" || sk.Description != "处理 PDF" || sk.WhenToUse != "用户要读 PDF 时" {
		t.Errorf("技能字段不对：%+v", sk)
	}
	if want := "skills/pdf/SKILL.md"; sk.Path != want {
		t.Errorf("path = %q，期望 %q", sk.Path, want)
	}
	if strings.HasPrefix(sk.Path, "/") || strings.Contains(sk.Path, dir) {
		t.Errorf("path 不能是绝对路径：%q", sk.Path)
	}
	if len(payload.Errors) != 1 {
		t.Fatalf("错误数 = %d，期望 1：%s", len(payload.Errors), body)
	}
	if !strings.Contains(payload.Errors[0], "broken") {
		t.Errorf("错误里没有点出坏技能：%q", payload.Errors[0])
	}
}

// 没有技能时两个字段都是 []，不是 null。
func TestSkillsIndexEmpty(t *testing.T) {
	_, h := newEnvServer(t)
	w, body := envRequest(t, h, "GET", "/api/bots/main/skills", "")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200：%s", w.Code, body)
	}
	var payload skillsPayload
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("响应不是技能索引 JSON: %v: %s", err, body)
	}
	if payload.Skills == nil || payload.Errors == nil {
		t.Fatalf("skills/errors 不应是 null：%s", body)
	}
	if len(payload.Skills) != 0 || len(payload.Errors) != 0 {
		t.Fatalf("空集合响应不对：%s", body)
	}
}

// 技能文件不可读时，errors 里的绝对路径被换成相对标签，不泄露 home/数据目录路径。
func TestSkillsIndexRedactsAbsolutePaths(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 无视文件权限，制造不出读取失败")
	}
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "data", "skills")
	writeBotSkill(t, skillsDir, "pdf", "处理 PDF", "用户要读 PDF 时", "第一步：先数页数。")
	locked := filepath.Join(skillsDir, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(locked, "SKILL.md")
	md := "---\nname: locked\ndescription: 读不到\nwhen_to_use: 测试\n---\n\n正文\n"
	if err := os.WriteFile(file, []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0); err != nil {
		t.Skipf("这个文件系统不支持 chmod 000：%v", err)
	}
	defer os.Chmod(file, 0o600)

	bs, err := OpenBots(Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bs.Close)

	w, body := envRequest(t, bs.Handler(nil), "GET", "/api/bots/main/skills", "")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200：%s", w.Code, body)
	}
	var payload skillsPayload
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("响应不是技能索引 JSON: %v: %s", err, body)
	}
	if len(payload.Errors) != 1 {
		t.Fatalf("错误数 = %d，期望 1：%s", len(payload.Errors), body)
	}
	msg := payload.Errors[0]
	if strings.Contains(msg, dir) {
		t.Errorf("错误里泄露了绝对路径 %q：%q", dir, msg)
	}
	if want := filepath.Join("skills", "locked", "SKILL.md"); !strings.Contains(msg, want) {
		t.Errorf("错误里没有相对标签 %q：%q", want, msg)
	}
}

// 技能索引只读：技能是 bot 级路径，顶层没有 GET /api/skills（避免和默认 Bot 混淆）。
func TestSkillsTopLevelNotServed(t *testing.T) {
	_, h := newEnvServer(t)
	w, resp := envRequest(t, h, "GET", "/api/skills", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("顶层 GET /api/skills 状态码 = %d，期望 404：%s", w.Code, resp)
	}
}
