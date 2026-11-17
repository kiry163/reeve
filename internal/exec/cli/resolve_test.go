//go:build !windows

package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kiry163/reeve/internal/exec"
)

func TestResolveFindsNvmInstallOutsidePath(t *testing.T) {
	home := fakeHome(t)
	// 新版本目录里没有 pi（例如 Node 跑不起来后退回旧版本），应该继续往下找到有 pi 的版本。
	if err := os.MkdirAll(filepath.Join(home, ".nvm/versions/node/v26.13.1/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".nvm/versions/node/v22.23.5/bin/pi")
	writeExe(t, want, "#!/bin/sh\n")
	writeExe(t, filepath.Join(home, ".nvm/versions/node/v9.1.0/bin/pi"), "#!/bin/sh\n")
	got, _, err := resolve(exec.KindPi)
	if err != nil || got != want {
		t.Fatalf("resolve(pi) = %q, %v; want %q", got, err, want)
	}
	if status := Detect(context.Background()); !status[1].Installed || status[1].ID != "pi" {
		t.Fatalf("pi should be detected: %+v", status[1])
	}
	if _, _, err := resolve(exec.KindCodex); err == nil {
		t.Fatal("codex is not installed and must not be found")
	}
}

func TestResolveUsesNpmrcPrefixAndSkipsNonExecutable(t *testing.T) {
	home := fakeHome(t)
	if err := os.WriteFile(filepath.Join(home, ".npmrc"), []byte("fund=false\nprefix=~/npm-root\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".local/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".local/bin/codex"), []byte("not executable"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "npm-root/bin/codex")
	writeExe(t, want, "#!/bin/sh\n")
	if got, _, err := resolve(exec.KindCodex); err != nil || got != want {
		t.Fatalf("resolve(codex) = %q, %v; want %q", got, err, want)
	}
}
