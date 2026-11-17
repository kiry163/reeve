//go:build linux

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemdUnitContainsExpectedDirectives(t *testing.T) {
	cfg := testConfig(t)
	out, err := renderSystemd(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ExecStart=" + cfg.ExecPath + " serve",
		"WorkingDirectory=" + cfg.DataDir,
		"Environment=REEVE_HOME=" + cfg.DataDir,
		"Restart=on-failure",
		"WantedBy=default.target",
		"StandardOutput=append:" + cfg.LogPath,
		"StandardError=append:" + cfg.LogPath,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("unit 缺少 %q\n%s", want, out)
		}
	}
}

func TestRenderReturnsUnitPath(t *testing.T) {
	cfg := testConfig(t)
	path, content, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "systemd", "user", "reeve.service")
	if path != wantPath {
		t.Fatalf("path=%q，期望 %q", path, wantPath)
	}
	if !strings.Contains(content, "Description=reeve assistant") {
		t.Fatalf("unit 内容不对：\n%s", content)
	}
}

func TestSystemdInstallIsIdempotent(t *testing.T) {
	cfg := testConfig(t)
	r := &fakeRunner{}
	first, err := install(context.Background(), cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Changed {
		t.Fatal("首次安装应 Changed=true")
	}
	if _, err := os.Stat(first.Path); err != nil {
		t.Fatalf("unit 未写入：%v", err)
	}
	if _, err := os.Stat(filepath.Dir(cfg.LogPath)); err != nil {
		t.Fatalf("日志目录未创建：%v", err)
	}
	if _, err := os.Stat(cfg.DataDir); err != nil {
		t.Fatalf("数据目录未创建：%v", err)
	}
	if !strings.Contains(r.calls[0], "--user daemon-reload") {
		t.Fatalf("应先 daemon-reload：%v", r.calls)
	}
	if !strings.Contains(r.calls[1], "enable --now reeve.service") {
		t.Fatalf("应 enable --now：%v", r.calls)
	}

	second, err := install(context.Background(), cfg, &fakeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed {
		t.Fatal("同样内容重复安装不应 Changed")
	}
}

func TestSystemdUnitEscapesSpecialCharacters(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	cfg := Config{
		Name:     "reeve",
		ExecPath: filepath.Join(base, "bin dir", "reeve $x"),
		DataDir:  filepath.Join(base, "data %dir"),
		LogPath:  filepath.Join(base, "logs %dir", "reeve.log"),
	}
	out, err := renderSystemd(cfg)
	if err != nil {
		t.Fatal(err)
	}

	exec := strings.ReplaceAll(strings.ReplaceAll(cfg.ExecPath, "%", "%%"), "$", "$$")
	for _, want := range []string{
		// ExecStart 含空格 → 加引号；$ 变 $$。
		"ExecStart=\"" + exec + "\" serve",
		// 路径类键不进入引号，% 变 %%。
		"WorkingDirectory=" + strings.ReplaceAll(cfg.DataDir, "%", "%%") + "\n",
		// Environment 值含空格 → 加引号，% 变 %%。
		"Environment=REEVE_HOME=\"" + strings.ReplaceAll(cfg.DataDir, "%", "%%") + "\"",
		"StandardOutput=append:" + strings.ReplaceAll(cfg.LogPath, "%", "%%"),
		"StandardError=append:" + strings.ReplaceAll(cfg.LogPath, "%", "%%"),
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("unit 缺少 %q\n%s", want, out)
		}
	}
	for _, bad := range []string{`WorkingDirectory="`, `StandardOutput=append:"`, `StandardError=append:"`} {
		if strings.Contains(out, bad) {
			t.Fatalf("路径类键不应加引号（%q）\n%s", bad, out)
		}
	}
}

func TestSystemdInstallAttemptsRestartWhenActiveAndChanged(t *testing.T) {
	cfg := testConfig(t)
	r := &fakeRunner{out: "active"}
	res, err := install(context.Background(), cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatal("首次安装应 Changed=true")
	}
	if !strings.Contains(joinCalls(r.calls), "try-restart reeve.service") {
		t.Fatalf("已 active 且内容变化时应 try-restart：%v", r.calls)
	}
}

func TestSystemdInstallReportsFailure(t *testing.T) {
	cfg := testConfig(t)
	r := &fakeRunner{failOn: "enable", failErr: errors.New("boom"), out: "denied"}
	_, err := install(context.Background(), cfg, r)
	if err == nil || !strings.Contains(err.Error(), "enable") || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("enable 失败应带输出返回错误，实际：%v", err)
	}
}

func TestSystemdUninstallMissingIsOK(t *testing.T) {
	cfg := testConfig(t)
	r := &fakeRunner{}
	res, err := uninstall(context.Background(), cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed || len(r.calls) != 0 {
		t.Fatalf("未安装时不应有变化或调用：%+v %v", res, r.calls)
	}
}

func TestSystemdUninstallInstalledRemovesUnit(t *testing.T) {
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
		t.Fatalf("unit 未删除：%v", err)
	}
	joined := joinCalls(r.calls)
	for _, want := range []string{"disable --now reeve.service", "daemon-reload"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("卸载缺少 %q：%v", want, r.calls)
		}
	}
}

func TestSystemdUninstallIgnoresNotLoadedButKeepsUnitOnRealFailure(t *testing.T) {
	cfg := testConfig(t)
	if _, err := install(context.Background(), cfg, &fakeRunner{}); err != nil {
		t.Fatal(err)
	}
	path, _, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}

	r := &fakeRunner{failOn: "disable", failErr: errors.New("exit 5"), out: "Unit reeve.service not loaded."}
	res, err := uninstall(context.Background(), cfg, r)
	if err != nil {
		t.Fatalf("未加载时 disable 失败应被忽略：%v", err)
	}
	if !res.Changed {
		t.Fatal("已安装时卸载应 Changed=true")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("未加载时 unit 应被删除：%v", err)
	}

	if _, err := install(context.Background(), cfg, &fakeRunner{}); err != nil {
		t.Fatal(err)
	}
	r = &fakeRunner{failOn: "disable", failErr: errors.New("exit 1"), out: "Failed to disable unit: Access denied"}
	if _, err := uninstall(context.Background(), cfg, r); err == nil {
		t.Fatal("disable 真实失败时卸载应报错")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("disable 失败时不应删除 unit：%v", err)
	}
}

func TestSystemdStatus(t *testing.T) {
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
	st, err = status(context.Background(), cfg, &fakeRunner{out: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Installed || !st.Loaded {
		t.Fatalf("active 状态不对：%+v", st)
	}
	st, err = status(context.Background(), cfg, &fakeRunner{out: "inactive"})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Installed || st.Loaded || st.Detail != "inactive" {
		t.Fatalf("inactive 状态不对：%+v", st)
	}
}

func TestSystemdRenderRejectsInvalidExtraEnv(t *testing.T) {
	cfg := testConfig(t)
	cfg.ExtraEnv = []string{"BAD KEY=x"}
	if _, _, err := Render(cfg); err == nil {
		t.Fatal("非法环境变量 key 应被拒绝")
	}
}

// install 必须先确认二进制可用，失败时不能留下 unit 等副作用。
func TestSystemdInstallRejectsMissingExecutable(t *testing.T) {
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
