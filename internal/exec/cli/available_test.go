//go:build !windows

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiry163/reeve/internal/exec"
)

// fakeLookup 让 runner 直接指向测试写的假 CLI。
func fakeLookup(exe string) Lookup {
	return func(exec.Kind) (string, []string, error) { return exe, nil, nil }
}

func TestAvailableReportsMissingBinary(t *testing.T) {
	r := NewPi().(*piRunner)
	r.look = func(exec.Kind) (string, []string, error) { return "", nil, os.ErrNotExist }
	if err := r.Available(context.Background()); err == nil || !strings.Contains(err.Error(), "找不到 pi") {
		t.Fatalf("缺少二进制应报错，得到 %v", err)
	}
}

func TestAvailableProbesVersion(t *testing.T) {
	cases := []struct {
		name    string
		runner  func(Lookup) error
		body    string
		wantVer string
		wantErr string
	}{
		{name: "codex", body: "#!/bin/sh\necho codex-cli 0.169.0\n", wantVer: "0.169.0",
			runner: func(l Lookup) error { return (&codexRunner{look: l}).Available(context.Background()) }},
		{name: "omp", body: "#!/bin/sh\necho omp/18.11.0\n", wantVer: "18.11.0",
			runner: func(l Lookup) error { return (&ompRunner{look: l}).Available(context.Background()) }},
		{name: "pi", body: "#!/bin/sh\necho 0.12.3\n", wantVer: "0.12.3",
			runner: func(l Lookup) error { return (&piRunner{look: l}).Available(context.Background()) }},
		// 退出码非零但输出了版本号：仍算可用（升级提示常混在 stderr）。
		{name: "pi", body: "#!/bin/sh\necho 'update available' >&2\necho 1.2.3\nexit 1\n", wantVer: "1.2.3",
			runner: func(l Lookup) error { return (&piRunner{look: l}).Available(context.Background()) }},
		// 跑得起来但没有版本号：装坏了。
		{name: "pi", body: "#!/bin/sh\nexit 0\n", wantErr: "可能装坏了",
			runner: func(l Lookup) error { return (&piRunner{look: l}).Available(context.Background()) }},
		{name: "omp", body: "#!/bin/sh\necho 'usage: omp [options]'\n", wantErr: "可能装坏了",
			runner: func(l Lookup) error { return (&ompRunner{look: l}).Available(context.Background()) }},
	}
	for i, c := range cases {
		t.Run(fmt.Sprintf("%s-%d", c.name, i), func(t *testing.T) {
			exe := writeScript(t, c.body)
			err := c.runner(fakeLookup(exe))
			if c.wantVer != "" {
				if err != nil {
					t.Fatalf("应可用，得到 %v", err)
				}
				got, err := probeVersion(context.Background(), exe, nil)
				if err != nil || got != c.wantVer {
					t.Fatalf("probeVersion = %q, %v；应为 %q", got, err, c.wantVer)
				}
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("应报 %q，得到 %v", c.wantErr, err)
			}
		})
	}
}

// 探测必须和真正执行一样把可执行文件目录放进 PATH：npm 装的 CLI 靠
// `#!/usr/bin/env node`，而服务进程的 PATH 里没有 reeve 自装的 Node。
func TestProbeVersionUsesExecutableDirOnPath(t *testing.T) {
	// 只在注入目录里存在的命令（本机 PATH 里没有同名项）。
	const helper = "reeve-fake-version-node"
	dir := t.TempDir()
	writeExe(t, filepath.Join(dir, helper), "#!/bin/sh\necho pi 0.12.3\n")
	body := "#!/bin/sh\n" + helper + " --version\n"
	exe := filepath.Join(dir, "pi")
	writeExe(t, exe, body)

	got, err := probeVersion(context.Background(), exe, nil)
	if err != nil || got != "0.12.3" {
		t.Fatalf("同目录的 node 没进 PATH：probeVersion = %q, %v", got, err)
	}

	// 牙齿：同样脚本放进没有 helper 的目录就必须失败（否则上一条断言没有意义）。
	bare := filepath.Join(t.TempDir(), "pi")
	writeExe(t, bare, body)
	if _, err := probeVersion(context.Background(), bare, nil); err == nil {
		t.Fatal("缺少同目录 node 时不应该探测成功")
	}
}

// reeve 一键安装的 CLI 在 <tools>/clis/bin，Node 在 <tools>/node/bin，两个目录都要能解析。
func TestProbeVersionFindsManagedNodeBin(t *testing.T) {
	tools := t.TempDir()
	SetToolsDir(tools)
	defer SetToolsDir("")
	const helper = "reeve-fake-managed-node"
	writeExe(t, filepath.Join(tools, "node", "bin", helper), "#!/bin/sh\necho omp 2.0.0\n")
	exe := filepath.Join(tools, "clis", "bin", "pi")
	writeExe(t, exe, "#!/bin/sh\n"+helper+" --version\n")

	got, err := probeVersion(context.Background(), exe, nil)
	if err != nil || got != "2.0.0" {
		t.Fatalf("reeve 自装的 Node 没进 PATH：probeVersion = %q, %v", got, err)
	}
}

// Windows 上 resolve 返回 node.exe + 入口脚本 prefix，探测要按 `exe prefix… --version` 跑。
func TestProbeVersionPassesPrefix(t *testing.T) {
	exe := writeScript(t, "#!/bin/sh\ncase \"$1\" in fake-entry.js) echo codex-cli 1.2.3;; esac\n")
	if _, err := probeVersion(context.Background(), exe, nil); err == nil {
		t.Fatal("没有 prefix 时脚本不应该输出版本号")
	}
	got, err := probeVersion(context.Background(), exe, []string{"fake-entry.js"})
	if err != nil || got != "1.2.3" {
		t.Fatalf("prefix 没传给 CLI：probeVersion = %q, %v", got, err)
	}
}

// Windows 上 codex 与 pi 可能共用同一个 node.exe，缓存键必须带上 prefix（JS 入口），
// 否则版本互串、一个的失败原因会盖到另一个可用的 CLI 上。
func TestProbeCacheSeparatesPrefixes(t *testing.T) {
	exe := writeScript(t, "#!/bin/sh\ncase \"$1\" in a.js) echo 'codex-cli 1.0.0';; b.js) echo 'codex-cli 2.0.0';; esac\n")
	if v, ok, _ := probeAndCache(context.Background(), exe, []string{"a.js"}); !ok || v != "1.0.0" {
		t.Fatalf("a.js 探测结果不对：%q %v", v, ok)
	}
	if v, ok, _ := probeAndCache(context.Background(), exe, []string{"b.js"}); !ok || v != "2.0.0" {
		t.Fatalf("b.js 探测结果不对：%q %v", v, ok)
	}
	// 第三个 prefix 是坏的；它失败不能污染前两个可用条目。
	if _, ok, note := probeAndCache(context.Background(), exe, []string{"broken.js"}); ok || note == "" {
		t.Fatalf("broken.js 应探测失败并带原因：ok=%v note=%q", ok, note)
	}
	for prefix, want := range map[string]string{"a.js": "1.0.0", "b.js": "2.0.0"} {
		if c, hit := cachedProbe(probeKey(exe, []string{prefix})); !hit || c.version != want || !c.available || c.note != "" {
			t.Fatalf("%s 的缓存被别的 prefix 污染了：%+v hit=%v", prefix, c, hit)
		}
	}
	if c, hit := cachedProbe(probeKey(exe, []string{"broken.js"})); !hit || c.available || c.note == "" {
		t.Fatalf("broken.js 应缓存自己的失败：%+v hit=%v", c, hit)
	}
	// 不带 prefix 是另一个键，不能命中带 prefix 的缓存。
	if _, hit := cachedProbe(probeKey(exe, nil)); hit {
		t.Fatal("不带 prefix 不该命中带 prefix 的缓存")
	}
}

// 前端 abort 会杀掉探测进程；这种假失败不能进缓存，否则正常 CLI 会「运行异常」10 秒。
func TestCancelledProbeNotCached(t *testing.T) {
	exe := writeScript(t, "#!/bin/sh\necho pi 0.9.9\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok, _ := probeAndCache(ctx, exe, nil); ok {
		t.Fatal("已取消的探测不该算可用")
	}
	if _, hit := cachedProbe(probeKey(exe, nil)); hit {
		t.Fatal("已取消的探测不该写缓存")
	}
	// 下一次正常探测必须真的执行（而不是读到缓存的失败）。
	if v, ok, _ := probeAndCache(context.Background(), exe, nil); !ok || v != "0.9.9" {
		t.Fatalf("取消后的重试没有真正执行：%q %v", v, ok)
	}
}

// DetectCached（?probe=0）只刷新安装状态，不探测；Detect 的探测结果复用 10 秒。
func TestDetectCachedDoesNotProbe(t *testing.T) {
	home := fakeHome(t)
	calls := filepath.Join(t.TempDir(), "calls")
	t.Setenv("REEVE_PROBE_CALLS", calls)
	writeExe(t, filepath.Join(home, ".local/bin/pi"),
		"#!/bin/sh\necho called >> \"$REEVE_PROBE_CALLS\"\necho 'pi 0.12.3'\n")

	piOf := func(statuses []Status) Status {
		t.Helper()
		for _, st := range statuses {
			if st.ID == "pi" {
				return st
			}
		}
		t.Fatal("缺少 pi")
		return Status{}
	}
	countCalls := func() int {
		t.Helper()
		raw, err := os.ReadFile(calls)
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(raw), "called")
	}

	if pi := piOf(DetectCached()); !pi.Installed || pi.Version != "" || pi.Available {
		t.Fatalf("probe=0 不该探测：%+v", pi)
	}
	if n := countCalls(); n != 0 {
		t.Fatalf("probe=0 起了 %d 次 --version", n)
	}
	if pi := piOf(Detect(context.Background())); !pi.Available || pi.Version != "0.12.3" {
		t.Fatalf("探测结果不对：%+v", pi)
	}
	// 10 秒内重复检测走缓存，不再起子进程。
	piOf(Detect(context.Background()))
	if n := countCalls(); n != 1 {
		t.Fatalf("缓存没生效，起了 %d 次 --version", n)
	}
	// probe=0 复用缓存里的版本，也仍然不探测。
	if pi := piOf(DetectCached()); pi.Version != "0.12.3" || !pi.Available {
		t.Fatalf("probe=0 应返回缓存版本：%+v", pi)
	}
	if n := countCalls(); n != 1 {
		t.Fatalf("probe=0 起了 %d 次 --version", n)
	}
}

// Detect 要把版本与可用性带给设置界面；坏掉的可执行文件要给出原因。
func TestDetectReportsVersionAndAvailability(t *testing.T) {
	home := fakeHome(t)
	writeExe(t, filepath.Join(home, ".local/bin/omp"), "#!/bin/sh\necho omp/18.11.0\n")
	writeExe(t, filepath.Join(home, ".local/bin/pi"), "#!/bin/sh\nexit 0\n")

	statuses := Detect(context.Background())
	if len(statuses) != 3 {
		t.Fatalf("应返回三个执行器：%+v", statuses)
	}
	byID := map[string]Status{}
	for _, st := range statuses {
		byID[st.ID] = st
	}
	if omp := byID["omp"]; !omp.Installed || !omp.Available || omp.Version != "18.11.0" || omp.Note != "" {
		t.Fatalf("omp 探测结果不对：%+v", omp)
	}
	if pi := byID["pi"]; !pi.Installed || pi.Available || pi.Version != "" || !strings.Contains(pi.Note, "可能装坏了") {
		t.Fatalf("坏掉的 pi 应不可用并带原因：%+v", pi)
	}
	if codex := byID["codex"]; codex.Installed || codex.Available || !strings.Contains(codex.Note, "找不到 codex") {
		t.Fatalf("没装的 codex 不该可用：%+v", codex)
	}
}
