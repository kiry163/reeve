// Package service 安装 reeve 为常驻用户服务：macOS 用 launchd，Linux 用 systemd --user。
// 生成与安装都接受 Config 里的全部路径，测试与调用方都不会碰真实家目录。
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kiry163/reeve/internal/atomicfile"
)

// ErrUnsupported 表示当前平台没有可用的用户级服务管理器。
var ErrUnsupported = errors.New("当前平台不支持安装服务（只支持 macOS 的 launchd 与 Linux 的 systemd --user）")

// Config 是一次服务安装的全部输入。所有路径都必须是绝对路径。
type Config struct {
	Name     string   // 服务名，用于文件名与 Label（dev.reeve.<Name>）
	ExecPath string   // reeve 可执行文件的绝对路径
	DataDir  string   // 数据目录，作为服务的工作目录与 REEVE_HOME
	LogPath  string   // 日志文件；为空时用 <DataDir>/logs/reeve.log
	ExtraEnv []string // 额外环境变量，形如 KEY=VALUE
}

// Result 描述一次安装/卸载做了什么。
type Result struct {
	Path     string
	Changed  bool
	Commands []string
}

// State 描述服务当前状态。
type State struct {
	Installed bool
	Loaded    bool
	Path      string
	Detail    string
}

// runner 执行外部命令；测试替换掉，不真的碰 launchctl/systemctl。
type runner interface {
	run(ctx context.Context, name string, args ...string) (string, error)
	lookup(name string) (string, error)
}

type execRunner struct{}

func (execRunner) run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// lookup 把工具名解析成绝对路径。只依赖 PATH 会让服务继承到错误的
// launchctl/systemctl（例如被篡改的 PATH），所以解析结果必须是绝对路径。
func (execRunner) lookup(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("找不到 %s：%w", name, err)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("找不到可用的 %s（PATH 解析结果 %q 不是绝对路径）", name, path)
	}
	return path, nil
}

// Install 生成并加载用户服务，重复执行是幂等的。
func Install(ctx context.Context, cfg Config) (Result, error) { return install(ctx, cfg, execRunner{}) }

// Uninstall 停止并移除用户服务；未安装时返回 Changed=false 且不报错。
func Uninstall(ctx context.Context, cfg Config) (Result, error) {
	return uninstall(ctx, cfg, execRunner{})
}

// Status 查询服务是否已安装、是否已加载。
func Status(ctx context.Context, cfg Config) (State, error) { return status(ctx, cfg, execRunner{}) }

// writeIfChanged 幂等写入：内容相同就不动文件，返回是否变化。
func writeIfChanged(path, content string) (bool, error) {
	if b, err := os.ReadFile(path); err == nil && string(b) == content {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := atomicfile.Write(path, []byte(content), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// EnsureDataDir 创建数据目录并保证权限是 0700，已存在的目录也会收紧。
// serve 与 service install 共用，两种执行先后顺序得到同一权限。
func EnsureDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("收紧数据目录权限失败：%w", err)
	}
	return nil
}

// ensureDirs 保证服务启动前 DataDir 与日志目录存在。
func ensureDirs(cfg Config) error {
	if err := EnsureDataDir(cfg.DataDir); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Dir(logPath(cfg)), 0o755)
}

// logPath 返回日志文件路径，没配置时落在数据目录的 logs 下。
func logPath(cfg Config) string {
	if cfg.LogPath != "" {
		return cfg.LogPath
	}
	return filepath.Join(cfg.DataDir, "logs", "reeve.log")
}

// commandError 把外部命令的失败与其输出拼成可诊断的错误。
func commandError(cmd, out string, err error) error {
	if out != "" {
		return fmt.Errorf("%s 失败：%w（%s）", cmd, err, out)
	}
	return fmt.Errorf("%s 失败：%w", cmd, err)
}

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// rejectLineBreaks 拒绝会破坏 plist/unit 单行结构的输入。
func rejectLineBreaks(field, value string) error {
	if strings.ContainsAny(value, "\n\r") {
		return fmt.Errorf("%s 不能包含换行符：%q", field, value)
	}
	return nil
}

func validate(cfg Config) error {
	if cfg.Name == "" || cfg.ExecPath == "" || cfg.DataDir == "" {
		return fmt.Errorf("服务配置不完整：需要 Name/ExecPath/DataDir")
	}
	for _, f := range []struct{ field, value string }{
		{"Name", cfg.Name},
		{"ExecPath", cfg.ExecPath},
		{"DataDir", cfg.DataDir},
	} {
		if err := rejectLineBreaks(f.field, f.value); err != nil {
			return err
		}
	}
	if cfg.LogPath != "" {
		if err := rejectLineBreaks("LogPath", cfg.LogPath); err != nil {
			return err
		}
	}
	if !filepath.IsAbs(cfg.ExecPath) {
		return fmt.Errorf("可执行文件必须是绝对路径：%s", cfg.ExecPath)
	}
	if !filepath.IsAbs(cfg.DataDir) {
		return fmt.Errorf("数据目录必须是绝对路径：%s", cfg.DataDir)
	}
	if cfg.LogPath != "" && !filepath.IsAbs(cfg.LogPath) {
		return fmt.Errorf("日志文件必须是绝对路径：%s", cfg.LogPath)
	}
	for _, kv := range cfg.ExtraEnv {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || !envKeyPattern.MatchString(key) {
			return fmt.Errorf("额外环境变量不合法：%q（需要 KEY=VALUE，KEY 只能含字母、数字、下划线且不能以数字开头）", kv)
		}
	}
	return nil
}

// checkExecutable 确认服务将启动的二进制存在且有可执行位。
// 只有在真正 install 时调用：--dry-run 允许路径尚未落地。
func checkExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("可执行文件不存在：%s（先构建或安装 reeve，或用 --exec 指定有效路径）", path)
		}
		return fmt.Errorf("检查可执行文件失败：%w", err)
	}
	if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("不是可执行文件：%s", path)
	}
	return nil
}
