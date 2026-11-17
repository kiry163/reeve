package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiry163/reeve/internal/exec"
)

// 模型名只能作为一个独立参数传给 CLI（绝不拼进 shell 字符串）。
func TestRunnersPassModelAsSeparateArgument(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	body := func(kind exec.Kind) string {
		return `#!/bin/sh
: > "$ARGS_FILE"
for a in "$@"; do printf '%s\n' "$a" >> "$ARGS_FILE"; done
` + runnerOutput(kind)
	}
	runners := map[exec.Kind]exec.Runner{
		exec.KindCodex: &codexRunner{},
		exec.KindPi:    &piRunner{},
		exec.KindOMP:   &ompRunner{},
	}
	for kind, runner := range runners {
		t.Run(string(kind), func(t *testing.T) {
			script := writeScript(t, body(kind))
			env := append(os.Environ(), "ARGS_FILE="+argsFile)
			switch r := runner.(type) {
			case *codexRunner:
				r.look = fixedLookup(script)
			case *piRunner:
				r.look = fixedLookup(script)
			case *ompRunner:
				r.look = fixedLookup(script)
			}
			for _, model := range []string{"m", ""} {
				_ = os.WriteFile(argsFile, nil, 0o600)
				h, err := runner.Start(context.Background(), exec.Request{TaskID: "t", Input: "task", Model: model,
					Workspace: dir, Env: env}, sinkFunc(func(exec.Event) {}))
				if err != nil {
					t.Fatal(err)
				}
				if res := <-h.Done(); res.Status != "completed" {
					t.Fatalf("%s model=%q: %+v", kind, model, res)
				}
				raw, err := os.ReadFile(argsFile)
				if err != nil {
					t.Fatal(err)
				}
				args := strings.Split(strings.TrimSpace(string(raw)), "\n")
				got := false
				for i := 0; i+1 < len(args); i++ {
					if args[i] == "--model" {
						got = true
						if args[i+1] != model {
							t.Fatalf("%s: --model 的值不是独立参数：%v", kind, args)
						}
					}
				}
				if got != (model != "") {
					t.Fatalf("%s model=%q: --model 出现情况不对：%v", kind, model, args)
				}
			}
		})
	}
}

func fixedLookup(path string) Lookup {
	return func(exec.Kind) (string, []string, error) { return path, nil, nil }
}

func runnerOutput(kind exec.Kind) string {
	switch kind {
	case exec.KindCodex:
		return "echo '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"ok\"}}'\n"
	case exec.KindPi:
		return "echo ok\n"
	default:
		return "echo '{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_delta\",\"delta\":\"ok\"}}'\n"
	}
}
