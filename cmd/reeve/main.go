// reeve：跑在自己电脑上的助理编排器。
//
//	reeve                 启动服务（等同 reeve serve）
//	reeve serve           启动 HTTP + SSE + 网页，默认只监听 127.0.0.1:18800
//	reeve pair            打印手机连接用的地址与 token
//	reeve token show      打印 token
//	reeve token rotate    轮换 token（所有已登录设备立即失效）
//	reeve service ...     安装/卸载/查询用户服务（launchd 或 systemd --user）
//	reeve version         版本信息
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

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
	case "token":
		tokenCmd(args)
	case "service":
		os.Exit(serviceCmd(args))
	case "version":
		fmt.Println("reeve", version)
	default:
		fmt.Fprintf(os.Stderr, "未知命令 %q；可用：serve / pair / token show / token rotate / service install|uninstall|status / version\n", cmd)
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
	if _, created, err := auth.EnsureToken(data); err != nil {
		fatal("准备访问 token 失败：%v", err)
	} else if created {
		log.Printf("已生成访问 token（%s/token，0600）", data)
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

	// TokenDir 让运行中的服务每次鉴权都重新读 token 文件，
	// 这样 reeve token rotate 能立即让旧 token 和旧会话失效，无需重启。
	guard := &auth.Guard{TokenDir: data, LoopbackFree: true, SessionTTL: 7 * 24 * time.Hour}
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

// printStartup 打印启动信息，返回浏览器应打开的页面地址（不含 token）。
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
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		fmt.Printf("\n  正在监听非回环地址：没有 TLS，仅限可信局域网；跨网络请用 Tailscale 或 SSH 隧道。\n")
		fmt.Printf("  手机连接需要 token：运行 reeve pair 获取完整地址。\n")
	}
	fmt.Printf("\n  按 Ctrl+C 退出。\n")
	return page
}

func pair(args []string) {
	fl := flag.NewFlagSet("pair", flag.ExitOnError)
	addr := fl.String("addr", envOr("REEVE_ADDR", "127.0.0.1:18800"), "手机能访问到的具体地址，例如 192.168.1.20:18800 或 Tailscale 地址")
	_ = fl.Parse(args)
	data := home()
	token, _, err := auth.EnsureToken(data)
	if err != nil {
		fatal("准备访问 token 失败：%v", err)
	}
	fmt.Printf("控制台： %s/\n", "http://"+*addr)
	fmt.Printf("配对串： reeve://pair?v=1&host=%s&token=%s\n", *addr, token)
}

func tokenCmd(args []string) {
	sub := "show"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "show":
		token, _, err := auth.EnsureToken(home())
		if err != nil {
			fatal("准备访问 token 失败：%v", err)
		}
		fmt.Println(token)
		fmt.Printf("配对串： reeve://pair?v=1&host=%s&token=%s\n", envOr("REEVE_ADDR", "127.0.0.1:18800"), token)
	case "rotate":
		next, err := auth.RotateToken(home())
		if err != nil {
			fatal("轮换 token 失败：%v", err)
		}
		fmt.Println("已轮换 token，所有已登录设备需重新配对。")
		fmt.Printf("新配对串： reeve://pair?v=1&host=%s&token=%s\n", envOr("REEVE_ADDR", "127.0.0.1:18800"), next)
	default:
		fmt.Fprintln(os.Stderr, "用法：reeve token show | reeve token rotate")
		os.Exit(2)
	}
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
