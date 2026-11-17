//go:build !darwin && !linux

package service

import (
	"context"
	"errors"
	"testing"
)

func TestUnsupportedPlatformReturnsErrUnsupported(t *testing.T) {
	cfg := Config{Name: "reeve", ExecPath: "/opt/reeve", DataDir: "/tmp/reeve"}
	if _, _, err := Render(cfg); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Render err=%v，期望 ErrUnsupported", err)
	}
	if _, err := install(context.Background(), cfg, &fakeRunner{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("install err=%v，期望 ErrUnsupported", err)
	}
	if _, err := uninstall(context.Background(), cfg, &fakeRunner{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("uninstall err=%v，期望 ErrUnsupported", err)
	}
	if _, err := status(context.Background(), cfg, &fakeRunner{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("status err=%v，期望 ErrUnsupported", err)
	}
}
