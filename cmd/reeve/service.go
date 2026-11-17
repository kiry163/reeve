package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/kiry163/reeve/internal/envstore"
	"github.com/kiry163/reeve/internal/service"
)

const serviceUsage = `用法：
  reeve service install [--exec <path>] [--dry-run]   安装并加载用户服务
  reeve service env refresh [--dry-run]                从当前 shell 导入 PATH/TZ/代理/TLS 到实例 env
  reeve service uninstall                              停止并移除用户服务
  reeve service status                                 查看是否已安装、是否已加载
`

func usageService(w io.Writer) { fmt.Fprint(w, serviceUsage) }

// serviceCmd 是 main 的入口，把退出码交给调用方。
func serviceCmd(args []string) int { return serviceRun(args, os.Stdout, os.Stderr) }

// serviceRun 可测试的实现：用法与错误写到 stderr，服务定义与结果写到 stdout。
func serviceRun(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usageService(stderr)
		return 2
	}
	if args[0] == "env" {
		return serviceEnvRun(args[1:], stdout, stderr)
	}
	action, rest := args[0], args[1:]

	fl := flag.NewFlagSet("service "+action, flag.ContinueOnError)
	fl.SetOutput(stderr)
	execPath := fl.String("exec", "", "reeve 可执行文件路径；默认取当前进程的可执行文件")
	dry := fl.Bool("dry-run", false, "只打印将要写入的服务定义，不落地")
	if err := fl.Parse(rest); err != nil {
		return 2
	}
	if fl.NArg() > 0 {
		fmt.Fprintf(stderr, "service %s 不接受多余参数：%s\n", action, strings.Join(fl.Args(), " "))
		usageService(stderr)
		return 2
	}

	// --exec/--dry-run 只对 install 有意义；其它 action 带上就报错，不能静默忽略。
	var usedExec, usedDry bool
	fl.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "exec":
			usedExec = true
		case "dry-run":
			usedDry = true
		}
	})
	if action != "install" {
		if usedDry {
			fmt.Fprintf(stderr, "service %s 不支持 --dry-run（只对 install 生效）\n", action)
			usageService(stderr)
			return 2
		}
		if usedExec {
			fmt.Fprintf(stderr, "service %s 不支持 --exec（只对 install 生效）\n", action)
			usageService(stderr)
			return 2
		}
	}

	data, _ := dataDirs()
	cfg := service.Config{
		Name:     "reeve",
		DataDir:  data,
		LogPath:  filepath.Join(data, "logs", "reeve.log"),
		ExecPath: *execPath,
	}
	if cfg.ExecPath == "" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintf(stderr, "找不到自身可执行文件：%v\n", err)
			return 1
		}
		cfg.ExecPath = exe
	}

	switch action {
	case "install":
		if *dry {
			path, content, err := service.Render(cfg)
			if err != nil {
				return serviceFail(stderr, "生成服务定义", err)
			}
			fmt.Fprintf(stderr, "将写入：%s\n", path)
			fmt.Fprint(stdout, content)
			return 0
		}
		// 服务管理器启动的进程只有最小环境；把当前用户 shell 的 PATH/时区/代理/TLS
		// 设置落进实例 env，任务执行器和 shell 才能拿到。
		created, updates, err := envstore.EnsureInstance(data)
		if err != nil {
			return serviceFail(stderr, "初始化服务环境变量", err)
		}
		if created {
			fmt.Fprintf(stdout, "已从当前 shell 初始化环境变量：%s/env（%d 项）\n", data, len(updates))
		} else {
			fmt.Fprintf(stdout, "使用已有环境变量文件：%s/env（可用 reeve service env refresh 更新）\n", data)
		}
		res, err := service.Install(context.Background(), cfg)
		if err != nil {
			return serviceFail(stderr, "安装服务", err)
		}
		fmt.Fprintf(stdout, "已安装：%s\n", res.Path)
		for _, c := range res.Commands {
			fmt.Fprintln(stdout, "  "+c)
		}
		return 0

	case "uninstall":
		res, err := service.Uninstall(context.Background(), cfg)
		if err != nil {
			return serviceFail(stderr, "卸载服务", err)
		}
		fmt.Fprintf(stdout, "已卸载：%s（变化=%v）\n", res.Path, res.Changed)
		for _, c := range res.Commands {
			fmt.Fprintln(stdout, "  "+c)
		}
		return 0

	case "status":
		st, err := service.Status(context.Background(), cfg)
		if err != nil {
			return serviceFail(stderr, "查询服务状态", err)
		}
		fmt.Fprintf(stdout, "已安装=%v 已加载=%v 路径=%s\n", st.Installed, st.Loaded, st.Path)
		if st.Detail != "" {
			fmt.Fprintln(stdout, st.Detail)
		}
		return 0

	default:
		fmt.Fprintf(stderr, "未知的 service 操作 %q\n", action)
		usageService(stderr)
		return 2
	}
}

// serviceEnvRun 实现 `reeve service env refresh [--dry-run]`。
func serviceEnvRun(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "refresh" {
		usageService(stderr)
		return 2
	}
	fl := flag.NewFlagSet("service env refresh", flag.ContinueOnError)
	fl.SetOutput(stderr)
	dry := fl.Bool("dry-run", false, "只显示将导入的变量，不写文件")
	if err := fl.Parse(args[1:]); err != nil {
		return 2
	}
	if fl.NArg() > 0 {
		fmt.Fprintf(stderr, "service env refresh 不接受多余参数：%s\n", strings.Join(fl.Args(), " "))
		usageService(stderr)
		return 2
	}
	data, _ := dataDirs()
	if *dry {
		updates := envstore.ShellEnv()
		printEnvUpdates(stdout, updates)
		if len(updates) == 0 {
			fmt.Fprintln(stdout, "当前 shell 没有可导入的 PATH/TZ/LANG/代理/TLS 变量。")
		} else {
			fmt.Fprintf(stdout, "未修改 %s/env\n", data)
		}
		return 0
	}
	updates, err := envstore.RefreshInstance(data)
	if err != nil {
		return serviceFail(stderr, "刷新服务环境变量", err)
	}
	if len(updates) == 0 {
		fmt.Fprintf(stdout, "当前 shell 没有可导入的 PATH/TZ/LANG/代理/TLS 变量；%s/env 未修改\n", data)
		return 0
	}
	printEnvUpdates(stdout, updates)
	fmt.Fprintf(stdout, "已更新 %s/env\n", data)
	return 0
}

// printEnvUpdates 按变量名排序打印 KEY=VALUE，便于用户确认导入了什么。
func printEnvUpdates(w io.Writer, updates map[string]string) {
	for _, key := range envstore.SortedKeys(updates) {
		fmt.Fprintf(w, "%s=%s\n", key, updates[key])
	}
}

// serviceFail 把不支持平台转成带平台名的友好提示；其它错误加上操作前缀。
func serviceFail(w io.Writer, op string, err error) int {
	if errors.Is(err, service.ErrUnsupported) {
		fmt.Fprintf(w, "reeve service 在当前平台（%s）不可用：%v\n", runtime.GOOS, err)
		return 1
	}
	fmt.Fprintf(w, "%s失败：%v\n", op, err)
	return 1
}
