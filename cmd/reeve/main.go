// reeve：跑在自己电脑上的助理编排器。
//
//	reeve                 启动服务（等同 reeve serve）
//	reeve serve           启动 HTTP + SSE + 网页，默认只监听 127.0.0.1:18800
//	reeve pair            打印手机连接用的配对地址
//	reeve passwd          重设登录密码（所有已登录设备立即失效）
//	reeve service ...     安装/卸载/查询用户服务（launchd 或 systemd --user）
//	reeve version         版本信息
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/kiry163/reeve/internal/auth"
	"github.com/kiry163/reeve/internal/bot"
	"github.com/kiry163/reeve/internal/service"
	"github.com/kiry163/reeve/web"
)

// version 由发布脚本用 -ldflags "-X main.version=..." 写入。
var version = "dev"

func main() {
	if version != "dev" {
		bot.Version = version // 让 /api/health 与更新检查显示同一个版本号
	}
	cmd := "serve"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		serve(args)
	case "pair":
		pair(args)
	case "passwd":
		passwdCmd(args)
	case "service":
		os.Exit(serviceCmd(args))
	case "version":
		fmt.Println("reeve", version)
	default:
		fmt.Fprintf(os.Stderr, "未知命令 %q；可用：serve / pair / passwd / service install|uninstall|status / version\n", cmd)
		os.Exit(2)
	}
}

// home 返回数据目录：REEVE_HOME 优先，否则 ~/.reeve。
func home() string {
	if v := strings.TrimSpace(os.Getenv("REEVE_HOME")); v != "" {
		return v
	}
	dir, err := os.UserHomeDir()
	if err != nil {
		fatal("找不到家目录：%v（可以设置 REEVE_HOME 指定数据目录）", err)
	}
	return filepath.Join(dir, ".reeve")
}

// dataDirs 返回数据目录和工作区；工作区固定放在数据目录下的 workspace。
func dataDirs() (data, workspace string) {
	data = home()
	return data, filepath.Join(data, "workspace")
}

func serve(args []string) {
	fl := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fl.String("addr", envOr("REEVE_ADDR", "127.0.0.1:18800"), "监听地址；要手机直连用 0.0.0.0:18800")
	_ = fl.Parse(args)

	data, workspace := dataDirs()
	if err := service.EnsureDataDir(data); err != nil {
		fatal("准备数据目录 %s 失败：%v", data, err)
	}
	initial, created, err := auth.EnsurePassword(data)
	if err != nil {
		fatal("准备登录密码失败：%v", err)
	}
	if created {
		fmt.Printf("\n  已生成初始登录密码：%s\n  请用这个密码登录；之后可在网页「设置 › 账号与登录」修改，或运行 reeve passwd 重设。\n\n", initial)
	}

	bots, err := bot.OpenBots(bot.Config{
		Name:      "reeve",
		DataDir:   data,
		Workspace: workspace,
		MasterKey: os.Getenv("REEVE_MASTER_KEY"),
	})
	if err != nil {
		fatal("启动失败：%v", err)
	}
	defer bots.Close()

	var ui fs.FS
	if d := strings.TrimSpace(os.Getenv("WEB_DIR")); d != "" {
		ui = os.DirFS(d)
	} else if emb, ok := web.Dist(); ok {
		ui = emb
	} else {
		ui = os.DirFS("web/dist")
	}

	// 所有请求（包括本机回环）都必须先登录；会话密钥每次鉴权都重新读取，
	// 所以 reeve passwd 或网页改密后，旧会话立即失效，不需要重启服务。
	guard := &auth.Guard{DataDir: data, SessionTTL: 30 * 24 * time.Hour}
	handler := guard.Middleware(bots.Handler(ui))

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fatal("监听 %s 失败：%v（端口被占用时换一个：reeve serve -addr 127.0.0.1:18801）", *addr, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	page := printStartup(ln.Addr().String(), data, workspace)
	if err := openBrowser(page); err != nil {
		fmt.Printf("  没能自动打开浏览器（%v），请手动打开上面的地址。\n", err)
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal("服务异常退出：%v", err)
		}
	}()
	<-ctx.Done()
	fmt.Println("\n正在退出…")
	sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		_ = srv.Close()
	}
}

// printStartup 打印启动信息，返回浏览器应打开的页面地址。
// addr 是实际监听地址（ln.Addr().String()），因此 localhost 之类的名字已经解析成数字 IP。
func printStartup(addr, data, workspace string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host, port = "127.0.0.1", "18800"
	}
	shown, open := host, host
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		shown, open = "<本机局域网地址>", "127.0.0.1"
	}
	page := "http://" + net.JoinHostPort(open, port) + "/"
	fmt.Printf("\n  reeve %s 已启动\n\n", version)
	fmt.Printf("  网页：    http://%s/\n", net.JoinHostPort(shown, port))
	fmt.Printf("  数据：    %s\n  工作区：  %s\n", data, workspace)
	fmt.Printf("  登录：    所有访问都需要密码；首次启动的初始密码只打印一次。\n")
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		fmt.Printf("\n  正在监听非回环地址：没有 TLS，仅限可信局域网；跨网络请用 Tailscale 或 SSH 隧道。\n")
		fmt.Printf("  手机连接：运行 reeve pair 获取配对地址，然后输入密码登录。\n")
	}
	fmt.Printf("\n  按 Ctrl+C 退出。\n")
	return page
}

func pair(args []string) {
	fl := flag.NewFlagSet("pair", flag.ExitOnError)
	addr := fl.String("addr", envOr("REEVE_ADDR", "127.0.0.1:18800"), "手机能访问到的具体地址，例如 192.168.1.20:18800 或 Tailscale 地址")
	_ = fl.Parse(args)
	data := home()
	initial, created, err := auth.EnsurePassword(data)
	if err != nil {
		fatal("准备登录密码失败：%v", err)
	}
	if created {
		fmt.Printf("初始登录密码：%s\n（只显示这一次；登录后可在网页设置里修改，或运行 reeve passwd 重设）\n\n", initial)
	}
	fmt.Printf("控制台： %s/\n", "http://"+*addr)
	fmt.Printf("配对串： reeve://pair?v=1&host=%s\n", *addr)
	fmt.Println("手机打开配对串后输入服务器密码登录。")
}

// passwdCmd 重设登录密码。能在服务器上读写数据目录就视为有权限，不要求旧密码。
func passwdCmd(args []string) {
	_ = args
	password, again, err := readNewPassword()
	if err != nil {
		fatal("读取密码失败：%v", err)
	}
	if password != again {
		fatal("两次输入的密码不一致")
	}
	if err := auth.SetPassword(home(), password); err != nil {
		fatal("设置密码失败：%v", err)
	}
	fmt.Println("密码已更新；所有已登录设备需要重新登录。")
}

// readNewPassword 优先隐藏终端输入；从管道读取时按行取两次（便于脚本使用）。
func readNewPassword() (string, string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Print("新密码：")
		first, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", "", err
		}
		fmt.Print("再次输入：")
		second, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", "", err
		}
		return string(first), string(second), nil
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	first := strings.TrimSpace(lines[0])
	second := first
	if len(lines) > 1 && strings.TrimSpace(lines[1]) != "" {
		second = strings.TrimSpace(lines[1])
	}
	return first, second, nil
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
