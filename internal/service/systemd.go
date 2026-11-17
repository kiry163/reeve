//go:build linux

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func unitPath(cfg Config) (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "systemd", "user", cfg.Name+".service"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("找不到家目录：%w", err)
	}
	return filepath.Join(home, ".config", "systemd", "user", cfg.Name+".service"), nil
}

// systemdPathValue 用于 WorkingDirectory/StandardOutput/StandardError：
// 这些键不是命令参数，不能加引号，只转义 systemd 的 % 说明符。
func systemdPathValue(s string) string {
	return strings.ReplaceAll(s, "%", "%%")
}

// systemdEscape 转义 % 说明符；ExecStart 里 $ 还会被当变量展开，需要再转义。
func systemdEscape(s string, exec bool) string {
	s = strings.ReplaceAll(s, "%", "%%")
	if exec {
		s = strings.ReplaceAll(s, "$", "$$")
	}
	return s
}

// quoteSystemd 在值含空白或引号时按 systemd 语法加双引号。
func quoteSystemd(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\"'\\") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func systemdValue(s string) string     { return quoteSystemd(systemdEscape(s, false)) }
func systemdExecValue(s string) string { return quoteSystemd(systemdEscape(s, true)) }

func renderSystemd(cfg Config) (string, error) {
	if err := validate(cfg); err != nil {
		return "", err
	}
	log := logPath(cfg)
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=reeve assistant\n")
	b.WriteString("After=network-online.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	fmt.Fprintf(&b, "ExecStart=%s serve\n", systemdExecValue(cfg.ExecPath))
	fmt.Fprintf(&b, "WorkingDirectory=%s\n", systemdPathValue(cfg.DataDir))
	fmt.Fprintf(&b, "Environment=REEVE_HOME=%s\n", systemdValue(cfg.DataDir))
	b.WriteString("Environment=PATH=/usr/local/bin:/usr/bin:/bin\n")
	for _, kv := range cfg.ExtraEnv {
		k, v, _ := strings.Cut(kv, "=")
		fmt.Fprintf(&b, "Environment=%s=%s\n", k, systemdValue(v))
	}
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=3\n")
	fmt.Fprintf(&b, "StandardOutput=append:%s\n", systemdPathValue(log))
	fmt.Fprintf(&b, "StandardError=append:%s\n\n", systemdPathValue(log))
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return b.String(), nil
}

// Render 返回 unit 的落盘路径与内容，供 --dry-run 使用。
func Render(cfg Config) (path, content string, err error) {
	content, err = renderSystemd(cfg)
	if err != nil {
		return "", "", err
	}
	path, err = unitPath(cfg)
	if err != nil {
		return "", "", err
	}
	return path, content, nil
}

func install(ctx context.Context, cfg Config, r runner) (Result, error) {
	path, content, err := Render(cfg)
	if err != nil {
		return Result{}, err
	}
	if err := checkExecutable(cfg.ExecPath); err != nil {
		return Result{}, err
	}
	systemctl, err := r.lookup("systemctl")
	if err != nil {
		return Result{}, err
	}
	if err := ensureDirs(cfg); err != nil {
		return Result{}, err
	}
	changed, err := writeIfChanged(path, content)
	if err != nil {
		return Result{}, err
	}
	res := Result{Path: path, Changed: changed}
	unit := cfg.Name + ".service"

	res.Commands = append(res.Commands, systemctl+" --user daemon-reload")
	if out, err := r.run(ctx, systemctl, "--user", "daemon-reload"); err != nil {
		return res, commandError("systemctl --user daemon-reload", out, err)
	}
	res.Commands = append(res.Commands, systemctl+" --user enable --now "+unit)
	if out, err := r.run(ctx, systemctl, "--user", "enable", "--now", unit); err != nil {
		return res, commandError("systemctl --user enable --now", out, err)
	}

	// 已运行的实例不会因 unit 内容更新而重启；与 launchd 的 bootout→bootstrap 对称。
	if changed {
		if out, err := r.run(ctx, systemctl, "--user", "is-active", unit); err == nil && strings.TrimSpace(out) == "active" {
			res.Commands = append(res.Commands, systemctl+" --user try-restart "+unit)
			if out, err := r.run(ctx, systemctl, "--user", "try-restart", unit); err != nil {
				return res, commandError("systemctl --user try-restart", out, err)
			}
		}
	}
	return res, nil
}

func uninstall(ctx context.Context, cfg Config, r runner) (Result, error) {
	path, err := unitPath(cfg)
	if err != nil {
		return Result{}, err
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{Path: path}, nil
		}
		return Result{}, err
	}
	unit := cfg.Name + ".service"
	systemctl, err := r.lookup("systemctl")
	if err != nil {
		return Result{}, err
	}
	res := Result{Path: path, Changed: true}

	// 未加载时 disable 会失败，只忽略这种情况；其它失败不删文件，避免留下无法管理的服务。
	res.Commands = append(res.Commands, systemctl+" --user disable --now "+unit)
	out, err := r.run(ctx, systemctl, "--user", "disable", "--now", unit)
	if err != nil && !isNotLoaded(out, err) {
		return res, commandError("systemctl --user disable --now", out, err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return res, err
	}
	res.Commands = append(res.Commands, systemctl+" --user daemon-reload")
	if out, err := r.run(ctx, systemctl, "--user", "daemon-reload"); err != nil {
		return res, commandError("systemctl --user daemon-reload", out, err)
	}
	return res, nil
}

// isNotLoaded 判断 systemctl 失败是否只是因为服务本来就没加载。
func isNotLoaded(out string, err error) bool {
	if err == nil {
		return false
	}
	for _, s := range []string{"not loaded", "not found", "does not exist", "No such file"} {
		if strings.Contains(out, s) || strings.Contains(err.Error(), s) {
			return true
		}
	}
	return false
}

func status(ctx context.Context, cfg Config, r runner) (State, error) {
	path, err := unitPath(cfg)
	if err != nil {
		return State{}, err
	}
	st := State{Path: path}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return st, nil
		}
		return st, err
	}
	st.Installed = true
	systemctl, err := r.lookup("systemctl")
	if err != nil {
		return st, err
	}
	out, err := r.run(ctx, systemctl, "--user", "is-active", cfg.Name+".service")
	if err != nil {
		st.Detail = out
		if st.Detail == "" {
			st.Detail = err.Error()
		}
		return st, nil
	}
	st.Loaded = out == "active"
	st.Detail = out
	return st, nil
}
