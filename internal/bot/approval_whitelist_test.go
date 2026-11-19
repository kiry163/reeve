package bot

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kiry163/reeve/internal/builtin"
)

func checkWhitelistDecision(t *testing.T, bs *Bots, cmd string) builtin.Decision {
	t.Helper()
	task := builtin.Task{ID: "t", Workspace: t.TempDir(), Policy: builtin.PolicyUntrusted}
	v, _ := bs.main.guard.Classify(task, builtin.ShellTool, json.RawMessage(`{"command":`+strconv.Quote(cmd)+`}`))
	return v.Decision
}

func TestApprovalWhitelistAPI(t *testing.T) {
	bs, h := newEnvServer(t)

	w, body := envRequest(t, h, http.MethodGet, "/api/approvals/whitelist", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", w.Code, body)
	}
	var view approvalWhitelistView
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatal(err)
	}
	if view.Commands == nil || len(view.Commands) != 0 {
		t.Fatalf("默认白名单应为空数组：%s", body)
	}

	// 保存时去掉首尾空白、空行和重复项。
	raw := `{"commands":["  go test*  ","git status","go test*",""]}`
	w, body = envRequest(t, h, http.MethodPut, "/api/approvals/whitelist", raw)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", w.Code, body)
	}
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(view.Commands, []string{"go test*", "git status"}) {
		t.Fatalf("归一化结果 = %v", view.Commands)
	}
	file := filepath.Join(bs.main.cfg.DataDir, "approval-whitelist.json")
	rawFile, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawFile), `"go test*"`) || !strings.Contains(string(rawFile), `"version": 1`) {
		t.Fatalf("白名单文件内容不对：%s", rawFile)
	}
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("白名单文件权限应为 0600：%v %v", st, err)
	}

	// 末尾 * 是前缀匹配；没有 * 时是整条命令精确匹配。
	if got := checkWhitelistDecision(t, bs, "go test ./..."); got != builtin.Allow {
		t.Fatalf("go test 前缀匹配 = %v", got)
	}
	if got := checkWhitelistDecision(t, bs, "go test -run TestX"); got != builtin.Allow {
		t.Fatalf("go test 带参数 = %v", got)
	}
	if got := checkWhitelistDecision(t, bs, "git status"); got != builtin.Allow {
		t.Fatalf("git status 精确匹配 = %v", got)
	}
	if got := checkWhitelistDecision(t, bs, "git status --short"); got != builtin.AskUser {
		t.Fatalf("未配置的变体应继续询问，得到 %v", got)
	}
	if got := checkWhitelistDecision(t, bs, "go test ./...; touch x"); got != builtin.AskUser {
		t.Fatalf("复合命令不应自动放行，得到 %v", got)
	}

	// 危险规则优先：即使白名单写成 git*，git push 仍然要审批。
	w, body = envRequest(t, h, http.MethodPut, "/api/approvals/whitelist", `{"commands":["git*"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT git* = %d: %s", w.Code, body)
	}
	if got := checkWhitelistDecision(t, bs, "git status"); got != builtin.Allow {
		t.Fatalf("git status = %v", got)
	}
	if got := checkWhitelistDecision(t, bs, "git push origin main"); got != builtin.AskUser {
		t.Fatalf("git push 应被危险规则拦住，得到 %v", got)
	}

	// 非法模式整体拒绝，不能写一半。
	for _, bad := range []string{
		`{"commands":["*"]}`,
		`{"commands":["go test; rm -rf /"]}`,
		`{"commands":["go test | cat"]}`,
		`{"commands":["go test $(pwd)"]}`,
		`{"commands":["go test ` + "`pwd`" + `"]}`,
		`{"commands":["go test\nrm -rf /"]}`,
	} {
		if w, body = envRequest(t, h, http.MethodPut, "/api/approvals/whitelist", bad); w.Code != http.StatusBadRequest {
			t.Fatalf("非法白名单 %s 应返回 400，得到 %d：%s", bad, w.Code, body)
		}
	}
	if got := checkWhitelistDecision(t, bs, "git status"); got != builtin.Allow {
		t.Fatalf("非法保存不应清掉旧规则，得到 %v", got)
	}

	// 空数组 = 清空。
	w, body = envRequest(t, h, http.MethodPut, "/api/approvals/whitelist", `{"commands":[]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("清空白名单 = %d: %s", w.Code, body)
	}
	if got := checkWhitelistDecision(t, bs, "git status"); got != builtin.AskUser {
		t.Fatalf("清空后应恢复询问，得到 %v", got)
	}
}

func TestApprovalWhitelistLoadsAtStartup(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"version":1,"commands":["go test*"]}`)
	if err := os.WriteFile(filepath.Join(dataDir, "approval-whitelist.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	bs, err := OpenBots(Config{DataDir: dataDir, Workspace: filepath.Join(dir, "ws")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bs.Close)
	if got := checkWhitelistDecision(t, bs, "go test ./..."); got != builtin.Allow {
		t.Fatalf("启动加载白名单失败，得到 %v", got)
	}
}

func TestApprovalWhitelistAppliesToNewBots(t *testing.T) {
	bs, _ := newEnvServer(t)
	if _, err := bs.saveApprovalWhitelist([]string{"go test*"}); err != nil {
		t.Fatal(err)
	}
	p, err := bs.setProfile("", BotProfile{Name: "Peer"})
	if err != nil {
		t.Fatal(err)
	}
	peer := bs.runtimes[p.ID]
	if peer == nil {
		t.Fatal("新 Bot 没有创建")
	}
	task := builtin.Task{ID: "t", Workspace: t.TempDir(), Policy: builtin.PolicyUntrusted}
	v, _ := peer.guard.Classify(task, builtin.ShellTool, json.RawMessage(`{"command":"go test ./..."}`))
	if v.Decision != builtin.Allow {
		t.Fatalf("新 Bot 没继承实例白名单，得到 %v", v.Decision)
	}
}

func TestCompileApprovalCommands(t *testing.T) {
	cleaned, patterns, err := compileApprovalCommands([]string{
		"  go test*  ", "git status", "go test*", "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cleaned, []string{"go test*", "git status"}) {
		t.Fatalf("cleaned = %v", cleaned)
	}
	if !slices.Equal(patterns, []string{`^go test.*$`, `^git status$`}) {
		t.Fatalf("patterns = %v", patterns)
	}
	for _, bad := range []string{"*", "go test;", "a|b", "$X", "a\nb", "a\tb"} {
		if _, _, err := compileApprovalCommands([]string{bad}); err == nil {
			t.Errorf("%q 应该被拒绝", bad)
		}
	}
	// 中间的 * 按字面量处理，只有行尾的 * 是前缀通配。
	_, patterns, err = compileApprovalCommands([]string{"find . -name '*.go'", "go * test", "**"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(patterns, []string{`^find \. -name '\*\.go'$`, `^go \* test$`, `^\*.*$`}) {
		t.Fatalf("中间的 * 应按字面量转义：%v", patterns)
	}
	// 正则元字符按字面量处理，不能变成通配。
	_, patterns, err = compileApprovalCommands([]string{"echo a.b"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(patterns, []string{`^echo a\.b$`}) {
		t.Fatalf("元字符没有转义：%v", patterns)
	}
}
