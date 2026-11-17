//go:build !windows

package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiry163/reeve/internal/exec"
)

func TestProcPauseResumeStop(t *testing.T) {
	script := writeLoopScript(t)
	ctx := context.Background()
	got := make(chan exec.Event, 256)
	p, err := Launch(ctx, Spec{Kind: exec.KindOMP, Exe: script, Dir: t.TempDir(), Env: os.Environ(),
		Parse: func(line []byte, sink exec.Sink, fail func(string)) {
			if strings.TrimSpace(string(line)) == "tick" {
				sink.Emit(exec.Event{Kind: exec.EventHeartbeat})
			}
		}}, sinkFunc(func(e exec.Event) { got <- e }))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Stop(context.Background()) }()
	// 本机 /bin/sh 启动要几百毫秒，不能靠固定 sleep：先等第一条 tick 证明脚本已在跑。
	waitEvents(t, got, 1, 5*time.Second)
	if err := p.Pause(ctx); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	drain(got)
	// 暂停后 400ms 内不应再有新事件（允许 1-2 条 SIGSTOP 瞬间在途的行）。
	time.Sleep(400 * time.Millisecond)
	if n := len(got); n > 2 {
		t.Fatalf("SIGSTOP 后仍在输出：%d 条", n)
	}
	if err := p.Resume(ctx); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	drain(got)
	waitEvents(t, got, 1, 3*time.Second)
	start := time.Now()
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case res := <-p.Done():
		if res.Status != "cancelled" {
			t.Fatalf("停止后状态应为 cancelled，得到 %q（err=%q）", res.Status, res.Error)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("Stop 后 8 秒仍未退出")
	}
	if d := time.Since(start); d > 7*time.Second {
		t.Fatalf("Stop 用了 %s，超过 TERM+5s+KILL 的预期", d)
	}
	if _, ok := <-p.Done(); ok {
		t.Fatal("Done 只应返回一次结果后关闭")
	}
}

func TestProcKillsStubbornProcess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stubborn.sh")
	// 忽略 TERM，只能靠 SIGKILL 结束。先打印 ready，测试等到它再 Stop：
	// 本机 /bin/sh 启动要几百毫秒，固定 sleep 会在 trap 生效之前就发 TERM。
	if err := os.WriteFile(path, []byte("#!/bin/sh\ntrap '' TERM\necho ready\nwhile true; do sleep 0.1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{}, 1)
	p, err := Launch(context.Background(), Spec{Kind: exec.KindOMP, Exe: path, Dir: dir, Env: os.Environ(),
		Parse: func(line []byte, _ exec.Sink, _ func(string)) {
			if strings.TrimSpace(string(line)) == "ready" {
				select {
				case ready <- struct{}{}:
				default:
				}
			}
		}}, sinkFunc(func(exec.Event) {}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("脚本没有进入忽略 TERM 的循环")
	}
	start := time.Now()
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case res := <-p.Done():
		if res.Status != "cancelled" {
			t.Fatalf("应为 cancelled，得到 %q", res.Status)
		}
	case <-time.After(9 * time.Second):
		t.Fatal("忽略 TERM 的进程未被 SIGKILL 终止")
	}
	if d := time.Since(start); d < 4*time.Second {
		t.Fatalf("Stop 太快（%s）：应先 TERM 并等 5 秒宽限", d)
	}
}

func TestProcHeartbeat(t *testing.T) {
	old := heartbeatInterval
	heartbeatInterval = 25 * time.Millisecond
	defer func() { heartbeatInterval = old }()

	beat := make(chan exec.Event, 8)
	p, err := Launch(context.Background(), Spec{Kind: exec.KindOMP,
		Exe: writeScript(t, "#!/bin/sh\nsleep 3\n"), Dir: t.TempDir(), Env: os.Environ(),
		Parse: func([]byte, exec.Sink, func(string)) {}}, sinkFunc(func(e exec.Event) {
		if e.Kind == exec.EventHeartbeat {
			beat <- e
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Stop(context.Background()) }()
	select {
	case <-beat:
	case <-time.After(2 * time.Second):
		t.Fatal("没有收到心跳")
	}
}

func TestProcCapturesTextOutput(t *testing.T) {
	p, err := Launch(context.Background(), Spec{Kind: exec.KindPi,
		Exe: writeScript(t, "#!/bin/sh\nprintf 'hello\\nworld\\n'\n"), Dir: t.TempDir(), Env: os.Environ(),
		Parse: func(line []byte, sink exec.Sink, fail func(string)) {
			sink.Emit(exec.Event{Kind: exec.EventText, Text: string(line) + "\n"})
		}}, sinkFunc(func(exec.Event) {}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-p.Done():
		if res.Status != "completed" {
			t.Fatalf("状态应为 completed，得到 %q（err=%q）", res.Status, res.Error)
		}
		if res.Output != "hello\nworld" {
			t.Fatalf("输出不符：%q", res.Output)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("进程没有退出")
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("已退出进程的 Stop 应返回 nil：%v", err)
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop 应幂等：%v", err)
	}
}

func TestProcFailedStderr(t *testing.T) {
	p, err := Launch(context.Background(), Spec{Kind: exec.KindCodex,
		Exe: writeScript(t, "#!/bin/sh\necho 'boom: bad login' >&2\nexit 3\n"), Dir: t.TempDir(), Env: os.Environ(),
		Parse: func([]byte, exec.Sink, func(string)) {}}, sinkFunc(func(exec.Event) {}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-p.Done():
		if res.Status != "failed" || !strings.Contains(res.Error, "boom: bad login") {
			t.Fatalf("失败详情应取 stderr：%#v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("进程没有退出")
	}
}

func TestProcEmptyReply(t *testing.T) {
	p, err := Launch(context.Background(), Spec{Kind: exec.KindOMP,
		Exe: writeScript(t, "#!/bin/sh\nexit 0\n"), Dir: t.TempDir(), Env: os.Environ(),
		Parse: func([]byte, exec.Sink, func(string)) {}}, sinkFunc(func(exec.Event) {}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-p.Done():
		if res.Status != "failed" || !strings.Contains(res.Error, "没有返回回复") {
			t.Fatalf("空回复应判失败：%#v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("进程没有退出")
	}
}

// 单行输出超过 1 MiB 时扫描器会停下：必须抽干管道，否则子进程写满管道阻塞，Done 永远不返回。
func TestProcOversizedLineDoesNotHang(t *testing.T) {
	p, err := Launch(context.Background(), Spec{Kind: exec.KindOMP,
		Exe: writeScript(t, "#!/bin/sh\nyes x | head -c 3145728 | tr -d '\\n'\necho\necho after\n"),
		Dir: t.TempDir(), Env: os.Environ(),
		Parse: func(line []byte, sink exec.Sink, fail func(string)) {
			sink.Emit(exec.Event{Kind: exec.EventText, Text: string(line) + "\n"})
		}}, sinkFunc(func(exec.Event) {}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Stop(context.Background()) }()
	select {
	case res := <-p.Done():
		if res.Status != "failed" || !strings.Contains(res.Error, "1 MiB") {
			t.Fatalf("超长行应判 failed 并给出诊断：%#v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("超长行把 Done 卡住了")
	}
}

// 适配器诊断只在进程非零退出且没有 stderr 时补进错误文案。
func TestProcUsesDiagnosticWhenStderrEmpty(t *testing.T) {
	p, err := Launch(context.Background(), Spec{Kind: exec.KindCodex,
		Exe: writeScript(t, "#!/bin/sh\nexit 3\n"), Dir: t.TempDir(), Env: os.Environ(),
		Parse: func([]byte, exec.Sink, func(string)) {},
		Diagnostic: func() string {
			return "Reconnecting... 5/5 (unexpected status 502 Bad Gateway)"
		}}, sinkFunc(func(exec.Event) {}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-p.Done():
		if res.Status != "failed" || !strings.Contains(res.Error, "Reconnecting... 5/5") {
			t.Fatalf("诊断没有进入错误文案：%#v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("进程没有退出")
	}
}

// Launch 只剥 reeve 自身凭据：厂商 Key 归 CommandEnv 管，auth=env 时必须能到子进程。
func TestProcStripsReeveCredentials(t *testing.T) {
	dir := t.TempDir()
	p, err := Launch(context.Background(), Spec{Kind: exec.KindOMP,
		Exe: writeScript(t, "#!/bin/sh\nenv > env.txt\n"), Dir: dir,
		Env: append(os.Environ(), "KEEP=1", "REEVE_MASTER_KEY=secret", "CHAT_API_KEY=secret",
			"OPENAI_API_KEY=sk-test"),
		Parse: func([]byte, exec.Sink, func(string)) {}}, sinkFunc(func(exec.Event) {}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("进程没有退出")
	}
	data, err := os.ReadFile(filepath.Join(dir, "env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	envText := string(data)
	for _, secret := range []string{"REEVE_MASTER_KEY", "CHAT_API_KEY"} {
		if strings.Contains(envText, secret) {
			t.Fatalf("reeve 凭据 %s 泄漏给子进程", secret)
		}
	}
	if !strings.Contains(envText, "KEEP=1") {
		t.Fatal("调用方传入的普通环境变量应保留")
	}
	if !strings.Contains(envText, "OPENAI_API_KEY=sk-test") {
		t.Fatal("厂商 Key 应留给 CommandEnv 决定，Launch 不能删")
	}
}

// 进程已经收尸后，Pause/Resume/Stop 都不再朝进程组发信号（PID/进程组复用的窗口）。
func TestSignalsIgnoredAfterExit(t *testing.T) {
	p, err := Launch(context.Background(), Spec{Kind: exec.KindOMP,
		Exe:   writeScript(t, "#!/bin/sh\nexit 0\n"),
		Parse: func([]byte, exec.Sink, func(string)) {}}, sinkFunc(func(exec.Event) {}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("进程没有退出")
	}
	if err := p.Pause(context.Background()); err != nil {
		t.Fatalf("Pause 对已退出的进程应为空操作：%v", err)
	}
	if err := p.Resume(context.Background()); err != nil {
		t.Fatalf("Resume 对已退出的进程应为空操作：%v", err)
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop 对已退出的进程应为空操作：%v", err)
	}
	p.mu.Lock()
	stopping := p.stopping
	p.mu.Unlock()
	if stopping {
		t.Fatal("进程退出后 Stop 不该再进入 stopping：否则会朝可能被复用的进程组发信号")
	}
}
