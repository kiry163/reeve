//go:build darwin

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchdPlistContainsAbsolutePathsAndKeepAlive(t *testing.T) {
	cfg := testConfig(t)
	out, err := renderLaunchd(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<key>Label</key>", "<string>dev.reeve.reeve</string>",
		"<key>ProgramArguments</key>", "<string>" + cfg.ExecPath + "</string>", "<string>serve</string>",
		"<key>WorkingDirectory</key>", "<string>" + cfg.DataDir + "</string>",
		"<key>RunAtLoad</key>", "<true/>",
		"<key>KeepAlive</key>", "<key>SuccessfulExit</key>",
		"<key>StandardOutPath</key>", "<key>StandardErrorPath</key>", "<string>" + cfg.LogPath + "</string>",
		"<key>EnvironmentVariables</key>", "<key>REEVE_HOME</key>",
		"<key>PATH</key>", "<string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("plist 缺少 %q\n%s", want, out)
		}
	}
	if n := strings.Count(out, "<key>EnvironmentVariables</key>"); n != 1 {
		t.Fatalf("EnvironmentVariables 应恰好出现 1 次，实际 %d 次\n%s", n, out)
	}
}

func TestRenderReturnsPlistPathAndEscapesValues(t *testing.T) {
	cfg := testConfig(t)
	cfg.ExtraEnv = []string{"FOO=bar", "MOTTO=<a&b>"}
	path, content, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", "reeve.plist")
	if path != wantPath {
		t.Fatalf("path=%q，期望 %q", path, wantPath)
	}
	for _, want := range []string{"<key>FOO</key>", "<string>bar</string>", "<string>&lt;a&amp;b&gt;</string>"} {
		if !strings.Contains(content, want) {
			t.Fatalf("plist 缺少 %q\n%s", want, content)
		}
	}
}

func TestInstallIsIdempotentAndUsesConfigPaths(t *testing.T) {
	cfg := testConfig(t)
	r := &fakeRunner{}
	first, err := install(context.Background(), cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Changed {
		t.Fatal("首次安装应 Changed=true")
	}
	info1, err := os.Stat(first.Path)
	if err != nil {
		t.Fatalf("plist 未写入：%v", err)
	}
	if _, err := os.Stat(filepath.Dir(cfg.LogPath)); err != nil {
		t.Fatalf("日志目录未创建：%v", err)
	}
	if !strings.Contains(first.Path, filepath.Join("Library", "LaunchAgents")) {
		t.Fatalf("plist 路径不对：%s", first.Path)
	}

	second, err := install(context.Background(), cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed {
		t.Fatal("同样内容重复安装不应 Changed")
	}
	info2, err := os.Stat(second.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Fatal("内容未变时不应重写 plist")
	}

	if _, err := os.Stat(cfg.DataDir); err != nil {
		t.Fatalf("数据目录未创建：%v", err)
	}

	joined := joinCalls(r.calls)
	for _, want := range []string{"bootout", "enable", "bootstrap"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("未调用 launchctl %s：%v", want, r.calls)
		}
	}
	if !strings.Contains(r.calls[0], "bootout") || !strings.Contains(r.calls[1], "enable") || !strings.Contains(r.calls[2], "bootstrap") {
		t.Fatalf("首次安装命令顺序应为 bootout→enable→bootstrap：%v", r.calls[:3])
	}
}

func TestInstallEnablesBeforeBootstrapWhenDisabled(t *testing.T) {
	cfg := testConfig(t)
	enabled := false
	r := &fakeRunner{hook: func(line string) (string, error) {
		switch {
		case strings.Contains(line, " enable "):
			enabled = true
		case strings.Contains(line, "bootstrap") && !enabled:
			return "Service is disabled", errors.New("exit 113")
		}
		return "", nil
	}}
	res, err := install(context.Background(), cfg, r)
	if err != nil {
		t.Fatalf("存在 disabled override 时 install 应成功：%v", err)
	}
	if !strings.Contains(joinCalls(res.Commands), "enable") {
		t.Fatalf("enable 未执行：%v", res.Commands)
	}
}

func TestInstallIgnoresBootoutFailureButReportsBootstrapFailure(t *testing.T) {
	cfg := testConfig(t)

	r := &fakeRunner{failOn: "bootout", failErr: errors.New("exit 3"), out: "Boot-out failed: 3: No such process"}
	if _, err := install(context.Background(), cfg, r); err != nil {
		t.Fatalf("bootout 未加载失败应被忽略：%v", err)
	}

	// macOS 对未加载服务的启动失败实测文案，安装必须是幂等的。
	r = &fakeRunner{failOn: "bootout", failErr: errors.New("exit status 5"), out: "Boot-out failed: 5: Input/output error"}
	if _, err := install(context.Background(), cfg, r); err != nil {
		t.Fatalf("bootout EIO 未加载失败应被忽略：%v", err)
	}

	r = &fakeRunner{failOn: "bootout", failErr: errors.New("exit 1"), out: "Operation not permitted"}
	if _, err := install(context.Background(), cfg, r); err == nil {
		t.Fatal("bootout 非未加载失败应报错")
	}

	r = &fakeRunner{failOn: "bootstrap", failErr: errors.New("boom"), out: "detail"}
	_, err := install(context.Background(), cfg, r)
	if err == nil || !strings.Contains(err.Error(), "bootstrap") || !strings.Contains(err.Error(), "detail") {
		t.Fatalf("bootstrap 失败应带输出返回错误，实际：%v", err)
	}
}

func TestUninstallMissingIsOK(t *testing.T) {
	cfg := testConfig(t)
	r := &fakeRunner{}
	res, err := uninstall(context.Background(), cfg, r)
	if err != nil {
		t.Fatalf("不存在的服务卸载应成功：%v", err)
	}
	if res.Changed {
		t.Fatal("未安装时 Changed 应为 false")
	}
	if len(r.calls) != 0 {
		t.Fatalf("未安装时不应调用 launchctl：%v", r.calls)
	}
}

func TestUninstallInstalledRemovesPlist(t *testing.T) {
	cfg := testConfig(t)
	if _, err := install(context.Background(), cfg, &fakeRunner{}); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	res, err := uninstall(context.Background(), cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatal("已安装时卸载应 Changed=true")
	}
	if _, err := os.Stat(res.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plist 未删除：%v", err)
	}
	if !strings.Contains(joinCalls(r.calls), "bootout") {
		t.Fatalf("卸载应调用 launchctl bootout：%v", r.calls)
	}
}

func TestUninstallIgnoresNotLoadedButKeepsPlistOnRealFailure(t *testing.T) {
	cfg := testConfig(t)
	if _, err := install(context.Background(), cfg, &fakeRunner{}); err != nil {
		t.Fatal(err)
	}
	path, _, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}

	r := &fakeRunner{failOn: "bootout", failErr: errors.New("exit 3"), out: "Boot-out failed: 3: No such process"}
	res, err := uninstall(context.Background(), cfg, r)
	if err != nil {
		t.Fatalf("未加载时卸载应成功：%v", err)
	}
	if !res.Changed {
		t.Fatal("已安装时卸载应 Changed=true")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("未加载时 plist 应被删除：%v", err)
	}

	if _, err := install(context.Background(), cfg, &fakeRunner{}); err != nil {
		t.Fatal(err)
	}
	r = &fakeRunner{failOn: "bootout", failErr: errors.New("exit 1"), out: "Operation not permitted"}
	if _, err := uninstall(context.Background(), cfg, r); err == nil {
		t.Fatal("bootout 真实失败时卸载应报错")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("bootout 失败时不应删除 plist：%v", err)
	}
}

func TestStatusReportsMissingAndLoaded(t *testing.T) {
	cfg := testConfig(t)
	st, err := status(context.Background(), cfg, &fakeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Installed || st.Loaded {
		t.Fatalf("未安装时状态不对：%+v", st)
	}

	if _, err := install(context.Background(), cfg, &fakeRunner{}); err != nil {
		t.Fatal(err)
	}
	st, err = status(context.Background(), cfg, &fakeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Installed || !st.Loaded {
		t.Fatalf("安装后状态不对：%+v", st)
	}

	st, err = status(context.Background(), cfg, &fakeRunner{failOn: "print", failErr: errors.New("no such service"), out: "not found"})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Installed || st.Loaded || !strings.Contains(st.Detail, "not found") {
		t.Fatalf("未加载时状态不对：%+v", st)
	}
}

func TestRenderRejectsIncompleteConfig(t *testing.T) {
	cfg := testConfig(t)
	cfg.ExecPath = "bin/reeve"
	if _, _, err := Render(cfg); err == nil {
		t.Fatal("相对路径应被拒绝")
	}
	cfg = testConfig(t)
	cfg.Name = ""
	if _, _, err := Render(cfg); err == nil {
		t.Fatal("缺少 Name 应被拒绝")
	}
	cfg = testConfig(t)
	cfg.LogPath = "logs/reeve.log"
	if _, _, err := Render(cfg); err == nil {
		t.Fatal("相对日志路径应被拒绝")
	}
}

func TestLaunchdRenderRejectsInvalidExtraEnv(t *testing.T) {
	cfg := testConfig(t)
	cfg.ExtraEnv = []string{"1BAD=x"}
	if _, _, err := Render(cfg); err == nil {
		t.Fatal("非法环境变量 key 应被拒绝")
	}
}

// install 必须先确认二进制可用，失败时不能留下数据目录等副作用。
func TestLaunchdInstallRejectsMissingExecutable(t *testing.T) {
	cfg := testConfig(t)
	cfg.ExecPath = filepath.Join(t.TempDir(), "missing-reeve")
	_, err := install(context.Background(), cfg, &fakeRunner{})
	if err == nil || !strings.Contains(err.Error(), "可执行文件不存在") {
		t.Fatalf("缺失的可执行文件应报错，实际：%v", err)
	}
	if _, statErr := os.Stat(cfg.DataDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("install 失败不应创建数据目录：%v", statErr)
	}
}
