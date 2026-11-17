package commands

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func noopRun(context.Context, Input) Result { return Result{OK: true} }

func TestLookupByNameAliasAndCase(t *testing.T) {
	r := NewRegistry()
	r.register(Definition{Name: "env", Aliases: []string{"e"}, Run: noopRun})

	for _, name := range []string{"env", "ENV", "Env", "e", "E"} {
		if d, ok := r.Lookup(name); !ok || d.Name != "env" {
			t.Fatalf("Lookup(%q) = %+v, %v；期望找到 env", name, d, ok)
		}
	}
	if _, ok := r.Lookup("nope"); ok {
		t.Fatal("Lookup(nope) 不应命中")
	}
}

func TestRegisterRejectsDuplicates(t *testing.T) {
	for _, tc := range []struct {
		name string
		defs []Definition
	}{
		{"重复名字", []Definition{{Name: "env"}, {Name: "env"}}},
		{"别名与名字冲突", []Definition{{Name: "env", Aliases: []string{"e"}}, {Name: "e"}}},
		{"重复别名", []Definition{{Name: "env", Aliases: []string{"e"}}, {Name: "edit", Aliases: []string{"E"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("重复注册应 panic")
				}
			}()
			r := NewRegistry()
			for _, d := range tc.defs {
				r.register(d)
			}
		})
	}
}

func TestLookupCopiesAliases(t *testing.T) {
	r := NewRegistry()
	r.register(Definition{Name: "env", Aliases: []string{"e", "ev"}, Run: noopRun})

	d, ok := r.Lookup("env")
	if !ok {
		t.Fatal("Lookup(env) 未命中")
	}
	d.Aliases[0] = "hacked"

	again, ok := r.Lookup("env")
	if !ok || !reflect.DeepEqual(again.Aliases, []string{"e", "ev"}) {
		t.Fatalf("修改 Lookup 返回值污染了注册表：%#v", again.Aliases)
	}
	if defs := r.Definitions(); !reflect.DeepEqual(defs[0].Aliases, []string{"e", "ev"}) {
		t.Fatalf("修改 Lookup 返回值影响了 Definitions：%#v", defs[0].Aliases)
	}
	if _, ok := r.Lookup("hacked"); ok {
		t.Fatal("Lookup(hacked) 不应命中")
	}
}

func TestDefinitionsKeepRegistrationOrderAndAreCopied(t *testing.T) {
	r := NewRegistry()
	r.register(Definition{Name: "help", Summary: "帮助", Run: noopRun})
	r.register(Definition{Name: "env", Aliases: []string{"e"}, Summary: "环境变量", Run: noopRun})

	defs := r.Definitions()
	if len(defs) != 2 || defs[0].Name != "help" || defs[1].Name != "env" {
		t.Fatalf("Definitions 顺序错误：%+v", defs)
	}

	defs[0].Name = "hacked"
	defs[1].Aliases[0] = "hacked"
	defs[0].Summary = "hacked"

	again := r.Definitions()
	if again[0].Name != "help" || again[0].Summary != "帮助" || again[1].Aliases[0] != "e" {
		t.Fatalf("修改返回值影响了注册表：%+v", again)
	}
	if _, ok := r.Lookup("help"); !ok {
		t.Fatal("修改返回值后 Lookup(help) 失效")
	}
	if _, ok := r.Lookup("hacked"); ok {
		t.Fatal("修改返回值后 Lookup(hacked) 不应命中")
	}
}

func TestHelpFormat(t *testing.T) {
	r := NewRegistry()
	r.register(Definition{
		Name: "env", Summary: "查看/修改环境变量",
		Usage: "/env [list|set K=V|unset K]",
		Run:   noopRun,
	})
	want := "/env  —  查看/修改环境变量\n  /env [list|set K=V|unset K]"
	if got := r.help(); got != want {
		t.Fatalf("help() = %q，期望 %q", got, want)
	}

	if r := NewRegistry(); r.help() != "" {
		t.Fatalf("空注册表 help() 应为空，得到 %q", r.help())
	}
}

func TestSummaryDoesNotNeedToBeUnique(t *testing.T) {
	r := NewRegistry()
	r.register(Definition{Name: "a", Summary: "同一句", Run: noopRun})
	r.register(Definition{Name: "b", Summary: "同一句", Run: noopRun})
	if got := r.help(); !strings.Contains(got, "/a  —  同一句") || !strings.Contains(got, "/b  —  同一句") {
		t.Fatalf("help() = %q", got)
	}
}
