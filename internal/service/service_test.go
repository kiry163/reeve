package service

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeRunner 记录命令而不真的调用 launchctl/systemctl。
type fakeRunner struct {
	calls   []string
	hook    func(line string) (out string, err error) // 非空时完全接管结果
	failOn  string                                    // 命令行包含此子串时返回 failErr
	failErr error
	out     string // 命中 failOn 或正常调用时返回的输出
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) (string, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, line)
	if f.hook != nil {
		return f.hook(line)
	}
	if f.failOn != "" && strings.Contains(line, f.failOn) {
		return f.out, f.failErr
	}
	return f.out, nil
}

// lookup 返回稳定的绝对路径，测试不依赖宿主机上是否装了 launchctl/systemctl。
func (f *fakeRunner) lookup(name string) (string, error) { return "/test/bin/" + name, nil }

// testConfig 把 HOME 与 XDG_CONFIG_HOME 都指到临时目录，测试绝不碰真实家目录。
func testConfig(t *testing.T) Config {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	execPath := filepath.Join(home, "bin", "reeve")
	if err := os.MkdirAll(filepath.Dir(execPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(execPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return Config{
		Name:     "reeve",
		ExecPath: execPath,
		DataDir:  filepath.Join(home, "data"),
		LogPath:  filepath.Join(home, "data", "logs", "reeve.log"),
	}
}

func joinCalls(calls []string) string { return strings.Join(calls, "\n") }

// install 与 serve 共用 EnsureDataDir：已存在的宽权限目录必须被收紧。
func TestEnsureDirsTightensExistingDataDir(t *testing.T) {
	cfg := testConfig(t)
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfg.DataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureDirs(cfg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("数据目录权限 %o，期望 0700", got)
	}
	if info, err := os.Stat(filepath.Dir(cfg.LogPath)); err != nil {
		t.Fatalf("日志目录未创建：%v", err)
	} else if info.Mode().Perm()&0o700 != 0o700 {
		t.Fatalf("日志目录权限 %o，至少应可读写执行", info.Mode().Perm())
	}
}

func TestEnsureDataDirCreatesWith0700(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "reeve")
	if err := EnsureDataDir(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("新建数据目录权限 %o，期望 0700", got)
	}
}

func TestValidateRejectsUnsafeInput(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantSub string
	}{
		{"换行 Name", func(c *Config) { c.Name = "reeve\nx" }, "Name"},
		{"回车 ExecPath", func(c *Config) { c.ExecPath = c.DataDir + "/\rreeve" }, "ExecPath"},
		{"换行 DataDir", func(c *Config) { c.DataDir = c.DataDir + "\nx" }, "DataDir"},
		{"换行 LogPath", func(c *Config) { c.LogPath += "\n" }, "LogPath"},
		{"env 数字开头", func(c *Config) { c.ExtraEnv = []string{"1BAD=x"} }, "额外环境变量"},
		{"env 带空格", func(c *Config) { c.ExtraEnv = []string{"BAD KEY=x"} }, "额外环境变量"},
		{"env 缺少等号", func(c *Config) { c.ExtraEnv = []string{"NOEQUALS"} }, "额外环境变量"},
		{"env 空 key", func(c *Config) { c.ExtraEnv = []string{"=x"} }, "额外环境变量"},
	}
	for _, tc := range cases {
		cfg := testConfig(t)
		tc.mutate(&cfg)
		err := validate(cfg)
		if err == nil {
			t.Fatalf("%s 应被拒绝", tc.name)
		}
		if !strings.Contains(err.Error(), tc.wantSub) {
			t.Fatalf("%s 的错误应提到 %s，实际：%v", tc.name, tc.wantSub, err)
		}
	}

	cfg := testConfig(t)
	cfg.ExtraEnv = []string{"PATH=/opt/x/bin", "_PRIVATE=1", "A1=2"}
	if err := validate(cfg); err != nil {
		t.Fatalf("合法配置不应报错：%v", err)
	}
}

func TestCheckExecutable(t *testing.T) {
	dir := t.TempDir()
	if err := checkExecutable(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("不存在的路径应报错")
	}
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkExecutable(plain); err == nil {
		t.Fatal("没有可执行位的文件应报错")
	}
	if err := checkExecutable(dir); err == nil {
		t.Fatal("目录应被拒绝")
	}
	good := filepath.Join(dir, "reeve")
	if err := os.WriteFile(good, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkExecutable(good); err != nil {
		t.Fatalf("可执行文件不应报错：%v", err)
	}
}

// 生产 runner 必须把工具名解析成绝对路径，否则可能执行到 PATH 里的冒名程序。
func TestExecRunnerLookupRequiresAbsolutePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("依赖 POSIX 的 sh")
	}
	r := execRunner{}
	path, err := r.lookup("sh")
	if err != nil {
		t.Fatalf("解析 sh 失败：%v", err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("lookup 应返回绝对路径，得到 %q", path)
	}
	if _, err := r.lookup("reeve-definitely-missing-tool"); err == nil {
		t.Fatal("找不到的工具应报错")
	}
}
