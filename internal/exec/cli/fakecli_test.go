//go:build !windows

package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kiry163/reeve/internal/exec"
)

// 假 CLI 夹具：临时目录里写一个可执行 shell 脚本。

func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake.sh")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// 假 CLI：每 100ms 打印一行，直到被杀。
func writeLoopScript(t *testing.T) string {
	t.Helper()
	return writeScript(t, "#!/bin/sh\nwhile true; do echo tick; sleep 0.1; done\n")
}

func drain(ch chan exec.Event) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func waitEvents(t *testing.T, got <-chan exec.Event, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for i := range n {
		select {
		case <-got:
		case <-deadline:
			t.Fatalf("等待第 %d 条事件超时", i+1)
		}
	}
}
