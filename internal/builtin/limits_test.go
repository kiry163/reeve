package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shell 输出超过 1 MiB 时截断并提示，不会把内存撑爆。
func TestShellOutputTruncatedAtOneMiB(t *testing.T) {
	ws := t.TempDir()
	args, _ := json.Marshal(map[string]string{"command": "head -c 2000000 /dev/zero | tr '\\0' a"})
	out, err := ShellTool.Execute(context.Background(), ToolEnv{Workspace: ws}, args)
	if err != nil {
		t.Skipf("shell unavailable: %v", err)
	}
	if !strings.Contains(out, "输出超过 1 MiB") {
		t.Fatalf("missing truncation notice (len %d)", len(out))
	}
	if len(out) > shellMaxOutput+200 {
		t.Fatalf("output not capped: %d bytes", len(out))
	}
}

// read_file 超过 10 MiB 必须先 stat 拒绝整体读取，但 offset/limit 仍可分段读。
func TestReadFileSizeGuard(t *testing.T) {
	ws := t.TempDir()
	p := filepath.Join(ws, "big.txt")
	if err := os.WriteFile(p, []byte(strings.Repeat("line\n", 3<<20)), 0o644); err != nil { // ~15 MiB
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"path": "big.txt"})
	_, err := readFileTool.Execute(context.Background(), ToolEnv{Workspace: ws}, raw)
	if err == nil || !strings.Contains(err.Error(), "10 MiB") || !strings.Contains(err.Error(), "offset/limit") {
		t.Fatalf("want size error, got %v", err)
	}
	raw, _ = json.Marshal(map[string]any{"path": "big.txt", "offset": 10})
	if _, err := readFileTool.Execute(context.Background(), ToolEnv{Workspace: ws}, raw); err == nil || !strings.Contains(err.Error(), "必须给 limit") {
		t.Fatalf("offset-only read of a big file should require limit, got %v", err)
	}
	raw, _ = json.Marshal(map[string]any{"path": "big.txt", "offset": 10, "limit": 3})
	out, err := readFileTool.Execute(context.Background(), ToolEnv{Workspace: ws}, raw)
	if err != nil {
		t.Fatalf("ranged read: %v", err)
	}
	if out != "line\nline\nline" {
		t.Fatalf("ranged read got %q", out)
	}
}
