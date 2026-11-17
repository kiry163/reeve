//go:build darwin

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func plistPath(cfg Config) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("找不到家目录：%w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", cfg.Name+".plist"), nil
}

func label(cfg Config) string { return "dev.reeve." + cfg.Name }

var xmlEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&apos;",
)

func xmlEscape(s string) string { return xmlEscaper.Replace(s) }

// renderLaunchd 生成 plist 内容。Render 再补上落盘路径。
func renderLaunchd(cfg Config) (string, error) {
	if err := validate(cfg); err != nil {
		return "", err
	}
	log := logPath(cfg)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	writeKey := func(k, v string) {
		fmt.Fprintf(&b, "  <key>%s</key>\n  <string>%s</string>\n", xmlEscape(k), xmlEscape(v))
	}
	writeKey("Label", label(cfg))
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	fmt.Fprintf(&b, "    <string>%s</string>\n    <string>serve</string>\n", xmlEscape(cfg.ExecPath))
	b.WriteString("  </array>\n")
	writeKey("WorkingDirectory", cfg.DataDir)
	b.WriteString("  <key>RunAtLoad</key>\n  <true/>\n")
	b.WriteString("  <key>KeepAlive</key>\n  <dict>\n    <key>SuccessfulExit</key>\n    <false/>\n  </dict>\n")
	writeKey("StandardOutPath", log)
	writeKey("StandardErrorPath", log)
	b.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
	b.WriteString("    <key>REEVE_HOME</key>\n")
	fmt.Fprintf(&b, "    <string>%s</string>\n", xmlEscape(cfg.DataDir))
	// launchd 默认 PATH 不含 Homebrew/本地 bin，外部执行器会找不到。
	b.WriteString("    <key>PATH</key>\n    <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>\n")
	for _, kv := range cfg.ExtraEnv {
		k, v, _ := strings.Cut(kv, "=")
		fmt.Fprintf(&b, "    <key>%s</key>\n    <string>%s</string>\n", xmlEscape(k), xmlEscape(v))
	}
	b.WriteString("  </dict>\n")
	b.WriteString("</dict>\n</plist>\n")
	return b.String(), nil
}

// Render 返回 plist 的落盘路径与内容，供 --dry-run 使用。
func Render(cfg Config) (path, content string, err error) {
	content, err = renderLaunchd(cfg)
	if err != nil {
		return "", "", err
	}
	path, err = plistPath(cfg)
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
	launchctl, err := r.lookup("launchctl")
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
	uid := strconv.Itoa(os.Getuid())
	domain := "gui/" + uid
	res := Result{Path: path, Changed: changed}

	// 先卸载旧定义再加载，保证内容更新后立即生效；未加载时 bootout 会失败，只忽略这种情况。
	res.Commands = append(res.Commands, launchctl+" bootout "+domain+" "+path)
	out, err := r.run(ctx, launchctl, "bootout", domain, path)
	if err != nil && !isNotLoaded(out, err) {
		return res, commandError("launchctl bootout", out, err)
	}

	// enable 必须早于 bootstrap：存在 disabled override 时 bootstrap 会直接失败。
	res.Commands = append(res.Commands, launchctl+" enable "+domain+"/"+label(cfg))
	if out, err := r.run(ctx, launchctl, "enable", domain+"/"+label(cfg)); err != nil {
		return res, commandError("launchctl enable", out, err)
	}

	res.Commands = append(res.Commands, launchctl+" bootstrap "+domain+" "+path)
	if out, err := r.run(ctx, launchctl, "bootstrap", domain, path); err != nil {
		return res, commandError("launchctl bootstrap", out, err)
	}
	return res, nil
}

func uninstall(ctx context.Context, cfg Config, r runner) (Result, error) {
	path, err := plistPath(cfg)
	if err != nil {
		return Result{}, err
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{Path: path}, nil
		}
		return Result{}, err
	}
	launchctl, err := r.lookup("launchctl")
	if err != nil {
		return Result{}, err
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	res := Result{Path: path, Changed: true}
	res.Commands = append(res.Commands, launchctl+" bootout "+domain+" "+path)
	out, err := r.run(ctx, launchctl, "bootout", domain, path)
	if err != nil && !isNotLoaded(out, err) {
		return res, commandError("launchctl bootout", out, err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return res, err
	}
	return res, nil
}

// isNotLoaded 判断 launchctl 失败是否只是因为服务本来就没加载。
// macOS 对未加载的 bootout 常回 "Boot-out failed: 5: Input/output error"，
// 既没有 "No such process" 也没有可辨认的 label，只能按这段文案识别。
func isNotLoaded(out string, err error) bool {
	if err == nil {
		return false
	}
	for _, s := range []string{"No such process", "Could not find service", "not loaded", "not found", "Input/output error", "Boot-out failed: 5"} {
		if strings.Contains(out, s) || strings.Contains(err.Error(), s) {
			return true
		}
	}
	return false
}

func status(ctx context.Context, cfg Config, r runner) (State, error) {
	path, err := plistPath(cfg)
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
	launchctl, err := r.lookup("launchctl")
	if err != nil {
		return st, err
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	out, err := r.run(ctx, launchctl, "print", domain+"/"+label(cfg))
	if err != nil {
		st.Detail = out
		if st.Detail == "" {
			st.Detail = err.Error()
		}
		return st, nil
	}
	st.Loaded = true
	st.Detail = "loaded"
	return st, nil
}
