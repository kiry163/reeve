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

// pair 只打印地址；首次运行会同时生成并打印一次初始密码。
func TestPairPrintsAddressWithoutSecret(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REEVE_HOME", dir)
	t.Setenv("REEVE_ADDR", "192.168.1.20:18800")
	got := captureStdout(t, func() { pair(nil) })
	if !strings.Contains(got, "控制台： http://192.168.1.20:18800/") {
		t.Fatalf("pair 应打印控制台地址：%q", got)
	}
	if !strings.Contains(got, "配对串： reeve://pair?v=1&host=192.168.1.20:18800") {
		t.Fatalf("pair 应打印只有地址的配对串：%q", got)
	}
	if strings.Contains(got, "token=") {
		t.Fatalf("配对串不能再携带密钥：%q", got)
	}
	if !strings.Contains(got, "初始登录密码：") {
		t.Fatalf("首次运行应打印初始密码：%q", got)
	}
	if st, err := os.Stat(filepath.Join(dir, "password")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("应生成 0600 的密码文件：%v", err)
	}
}

// passwd 从管道读取两行密码并重设；不需要旧密码。
func TestPasswdResetsPassword(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REEVE_HOME", dir)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("new-password\nnew-password\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old; r.Close() })
	captureStdout(t, func() { passwdCmd(nil) })
	if ok, err := auth.VerifyPassword(dir, "new-password"); err != nil || !ok {
		t.Fatalf("passwd 后新密码应可登录：%v %v", ok, err)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	fn()
	os.Stdout = old
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestServiceEnvRefreshDryRunDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REEVE_HOME", dir)
	t.Setenv("PATH", "/shell/bin:/usr/bin")
	t.Setenv("TZ", "UTC")
	stdout, stderr, code := runService(t, "env", "refresh", "--dry-run")
	if code != 0 {
		t.Fatalf("dry-run 应成功：code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "PATH=/shell/bin:/usr/bin") || !strings.Contains(stdout, "TZ=UTC") {
		t.Fatalf("应打印将导入的变量：%q", stdout)
	}
	if !strings.Contains(stdout, "未修改") {
		t.Fatalf("dry-run 应说明未修改：%q", stdout)
	}
	if _, err := os.Stat(filepath.Join(dir, "env")); !os.IsNotExist(err) {
		t.Fatalf("dry-run 不应写 env：%v", err)
	}
}

func TestServiceEnvRefreshWritesAndPreservesOtherKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REEVE_HOME", dir)
	t.Setenv("PATH", "/shell/bin:/usr/bin")
	if err := os.WriteFile(filepath.Join(dir, "env"), []byte("CUSTOM=keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runService(t, "env", "refresh")
	if code != 0 {
		t.Fatalf("refresh 应成功：code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "已更新") || !strings.Contains(stdout, "PATH=/shell/bin:/usr/bin") {
		t.Fatalf("应打印更新结果：%q", stdout)
	}
	data, err := os.ReadFile(filepath.Join(dir, "env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "CUSTOM=keep") || !strings.Contains(string(data), "PATH=/shell/bin:/usr/bin") {
		t.Fatalf("应保留其他键并导入 PATH：%q", data)
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
