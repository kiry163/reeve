package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kiry163/reeve/internal/auth"
	"github.com/kiry163/reeve/internal/service"
)

// runService 运行子命令并收集两个输出流，避免 os.Exit 打断测试。
func runService(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = serviceRun(args, &out, &errOut)
	return out.String(), errOut.String(), code
}

// 登录页文案引导用户运行 reeve token show：它必须打印当前 token 和配对串。
func TestTokenShowPrintsTokenAndPairString(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REEVE_HOME", dir)
	t.Setenv("REEVE_ADDR", "192.168.1.20:18800")
	token, _, err := auth.EnsureToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	tokenCmd([]string{"show"})
	os.Stdout = old
	w.Close()
	out, _ := io.ReadAll(r)
	got := string(out)
	if !strings.Contains(got, token) {
		t.Fatalf("token show should print the token: %q", got)
	}
	if !strings.Contains(got, "reeve://pair?v=1&host=192.168.1.20:18800&token="+token) {
		t.Fatalf("token show should print the pair string: %q", got)
	}
}

func TestServiceCommandRequiresAction(t *testing.T) {
	_, stderr, code := runService(t)
	if code != 2 {
		t.Fatalf("service 无参数应 exit 2，得到 code=%d", code)
	}
	if !strings.Contains(stderr, "install") || !strings.Contains(stderr, "uninstall") || !strings.Contains(stderr, "status") {
		t.Fatalf("用法应列出 install/uninstall/status，得到 %q", stderr)
	}
}

func TestServiceUnknownActionExitsTwo(t *testing.T) {
	_, stderr, code := runService(t, "bogus")
	if code != 2 {
		t.Fatalf("未知 action 应 exit 2，得到 code=%d", code)
	}
	if !strings.Contains(stderr, "install") || !strings.Contains(stderr, "bogus") {
		t.Fatalf("未知 action 应提示操作名与用法，得到 %q", stderr)
	}
}

func TestServiceInstallDryRunPrintsDefinitionAndPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REEVE_HOME", dir)

	stdout, stderr, code := runService(t, "install", "--dry-run", "--exec", "/usr/local/bin/reeve")
	if code != 0 {
		t.Fatalf("dry-run 应成功：code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "<key>Label</key>") && !strings.Contains(stdout, "[Service]") {
		t.Fatalf("dry-run 应打印服务定义：%s", stdout)
	}
	if !strings.Contains(stderr, "将写入：/") {
		t.Fatalf("dry-run 应把落盘路径打印到 stderr：%q", stderr)
	}
}

func TestServiceUnsupportedIsFriendly(t *testing.T) {
	var buf bytes.Buffer
	code := serviceFail(&buf, "安装服务", service.ErrUnsupported)
	if code != 1 {
		t.Fatalf("不支持平台应 exit 1，得到 %d", code)
	}
	if !strings.Contains(buf.String(), runtime.GOOS) || !strings.Contains(buf.String(), "不可用") {
		t.Fatalf("提示应含平台名与不可用说明：%q", buf.String())
	}
}

// fakeLaunchctl 在 PATH 最前面放一个假 launchctl，被调用时写标记文件。
func fakeLaunchctl(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "called")
	script := "#!/bin/sh\n: > " + marker + "\n"
	if err := os.WriteFile(filepath.Join(dir, "launchctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return marker
}

// --dry-run/--exec 只对 install 生效；其它 action 必须拒绝，不能静默忽略后真执行。
func TestServiceDryRunAndExecOnlyForInstall(t *testing.T) {
	data := t.TempDir()
	t.Setenv("REEVE_HOME", data)
	marker := fakeLaunchctl(t)

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"uninstall dry-run", []string{"uninstall", "--dry-run"}, "不支持 --dry-run"},
		{"status dry-run", []string{"status", "--dry-run"}, "不支持 --dry-run"},
		{"status exec", []string{"status", "--exec", "/usr/local/bin/reeve"}, "不支持 --exec"},
	}
	for _, tc := range cases {
		_, stderr, code := runService(t, tc.args...)
		if code != 2 {
			t.Fatalf("%s 应 exit 2，得到 code=%d stderr=%q", tc.name, code, stderr)
		}
		if !strings.Contains(stderr, tc.want) || !strings.Contains(stderr, "install") {
			t.Fatalf("%s 应报错并给出用法，得到 %q", tc.name, stderr)
		}
	}

	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("被拒绝的命令不应调用 launchctl（marker=%v）", err)
	}
	if entries, err := os.ReadDir(data); err != nil || len(entries) != 0 {
		t.Fatalf("被拒绝的命令不应有副作用：%v %v", entries, err)
	}
}

// dry-run 只渲染内容，不能创建数据目录，也不能碰 launchctl。
func TestServiceInstallDryRunHasNoSideEffects(t *testing.T) {
	data := t.TempDir()
	t.Setenv("REEVE_HOME", data)
	marker := fakeLaunchctl(t)

	_, stderr, code := runService(t, "install", "--dry-run", "--exec", "/usr/local/bin/reeve")
	if code != 0 {
		t.Fatalf("dry-run 应成功：code=%d stderr=%s", code, stderr)
	}
	if entries, err := os.ReadDir(data); err != nil || len(entries) != 0 {
		t.Fatalf("dry-run 不应创建数据目录内容：%v %v", entries, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("dry-run 不应调用 launchctl")
	}
}
