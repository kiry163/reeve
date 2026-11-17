package exec

import (
	"context"
	"errors"
	"testing"
)

type stubRunner struct {
	kind      Kind
	available error
}

func (s stubRunner) Kind() Kind                      { return s.kind }
func (s stubRunner) Available(context.Context) error { return s.available }
func (s stubRunner) Start(context.Context, Request, Sink) (Handle, error) {
	return nil, errors.New("not implemented")
}

func TestRegistryGetAndKinds(t *testing.T) {
	r := NewRegistry(stubRunner{kind: KindOMP}, stubRunner{kind: KindCodex})
	if _, ok := r.Get(KindOMP); !ok {
		t.Fatal("KindOMP 未注册")
	}
	kinds := r.Kinds()
	if len(kinds) != 2 || kinds[0] != KindCodex || kinds[1] != KindOMP {
		t.Fatalf("Kinds() 应按字母序返回 [codex omp]，得到 %v", kinds)
	}
	if _, ok := r.Get(KindPi); ok {
		t.Fatal("未注册的 KindPi 不应命中")
	}
}

func TestRegistryDuplicateKindPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("重复注册同一 Kind 应 panic，避免静默覆盖适配器")
		}
	}()
	NewRegistry(stubRunner{kind: KindPi}, stubRunner{kind: KindPi})
}

func TestKindIsStableString(t *testing.T) {
	// Kind 会写进事件日志与 executors.json，值不能变。
	for kind, want := range map[Kind]string{KindBuiltin: "builtin", KindCodex: "codex", KindPi: "pi", KindOMP: "omp"} {
		if string(kind) != want {
			t.Fatalf("%v 的字符串应为 %q", kind, want)
		}
	}
}
