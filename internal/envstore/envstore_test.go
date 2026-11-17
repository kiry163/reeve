package envstore

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestValidNameAndValue(t *testing.T) {
	for _, ok := range []string{"PATH", "A", "_X1", "NODE_OPTIONS"} {
		if err := ValidName(ok); err != nil {
			t.Fatalf("%s 应合法：%v", ok, err)
		}
	}
	for _, bad := range []string{"", "1A", "A-B", "A B", "A=B"} {
		if err := ValidName(bad); err == nil {
			t.Fatalf("%q 应非法", bad)
		}
	}
	if err := ValidValue("a\nb"); err == nil {
		t.Fatal("值含换行应非法")
	}
}

func TestValidNameLengthLimit(t *testing.T) {
	if err := ValidName(strings.Repeat("A", 128)); err != nil {
		t.Fatalf("128 字节的名字应合法：%v", err)
	}
	if err := ValidName(strings.Repeat("A", 129)); err == nil {
		t.Fatal("129 字节的名字应非法")
	}
}

func TestValidValueRejectsControlAndTooLong(t *testing.T) {
	for _, bad := range []string{"a\rb", "a\x00b", strings.Repeat("x", 8193)} {
		if err := ValidValue(bad); err == nil {
			t.Fatalf("%q 应非法", bad)
		}
	}
	if err := ValidValue(strings.Repeat("x", 8192)); err != nil {
		t.Fatalf("8192 字节的值应合法：%v", err)
	}
	if err := ValidValue(""); err != nil {
		t.Fatalf("空值应合法（表示删除）：%v", err)
	}
}

func TestMergeOverrideAndUnset(t *testing.T) {
	base := []string{"PATH=/usr/bin", "A=base", "B=keep"}
	got := Merge(base, map[string]string{"A": "bot", "C": "new"}, map[string]string{"C": "call", "B": ""})
	want := []string{"PATH=/usr/bin", "A=bot", "C=call"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestMergePathPrepends(t *testing.T) {
	got := Merge([]string{"PATH=/usr/bin"}, map[string]string{"PATH": "/opt/bin"}, map[string]string{"PATH": "/tmp/x"})
	want := "PATH=/tmp/x" + pathListSeparator() + "/opt/bin" + pathListSeparator() + "/usr/bin"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("PATH 应前置拼接，得到 %v，期望 %q", got, want)
	}
}

func TestMergeKeepsEqualSignInValue(t *testing.T) {
	got := Merge([]string{"URL=a=b"}, map[string]string{"URL": "c=d"})
	want := []string{"URL=c=d"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestMergeAddsNewKeysSortedAndDropsUnset(t *testing.T) {
	got := Merge(nil, map[string]string{"Z": "1", "A": "2", "M": "3", "B": "4", "Q": "5"})
	want := []string{"A=2", "B=4", "M=3", "Q=5", "Z=1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if got := Merge([]string{"A=1"}, map[string]string{"A": ""}); len(got) != 0 {
		t.Fatalf("被删除的键不应出现，得到 %v", got)
	}
	if got := Merge([]string{"PATH=/bin"}, map[string]string{"PATH": ""}); len(got) != 0 {
		t.Fatalf("PATH 置空应删除该键，得到 %v", got)
	}
}

func TestInstanceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	vars := map[string]string{"PATH": "/usr/bin", "HTTPS_PROXY": "http://127.0.0.1:7890"}
	if err := SaveInstance(dir, vars); err != nil {
		t.Fatal(err)
	}
	checkFileMode(t, filepath.Join(dir, "env"), 0o600)
	got, err := LoadInstance(dir)
	if err != nil || !reflect.DeepEqual(got, vars) {
		t.Fatalf("往返失败：%v %v", got, err)
	}
}

func TestInstanceFileFormatCommentsAndSpaces(t *testing.T) {
	dir := t.TempDir()
	content := "# 注释\n\nA=1\n  B = two words  \nURL=http://x/?a=b\n"
	if err := os.WriteFile(filepath.Join(dir, "env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "1", "B": "two words", "URL": "http://x/?a=b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestInstanceSaveIsSortedAndCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "inst")
	if err := SaveInstance(dir, map[string]string{"B": "2", "A": "1"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "A=1\nB=2\n" {
		t.Fatalf("应按 key 排序，得到 %q", data)
	}
}

func TestLoadMissingFilesGiveEmptyMap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nope")
	inst, err := LoadInstance(dir)
	if err != nil || inst == nil || len(inst) != 0 {
		t.Fatalf("缺失的实例级 env 应返回空 map 且无错误，得到 %v %v", inst, err)
	}
	bot, err := LoadBot(dir)
	if err != nil || bot == nil || len(bot) != 0 {
		t.Fatalf("缺失的 bot env.json 应返回空 map 且无错误，得到 %v %v", bot, err)
	}
}

func TestLoadInstanceRejectsMalformedLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "env"), []byte("A=1\nBROKEN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadInstance(dir); err == nil {
		t.Fatal("没有等号的行应报错")
	}
}

func TestBotRoundTrip(t *testing.T) {
	botDir := filepath.Join(t.TempDir(), "bots", "helper")
	vars := map[string]string{"Z_TOKEN": "z", "A_FLAG": "1"}
	if err := SaveBot(botDir, vars); err != nil {
		t.Fatal(err)
	}
	checkFileMode(t, filepath.Join(botDir, "env.json"), 0o600)
	got, err := LoadBot(botDir)
	if err != nil || !reflect.DeepEqual(got, vars) {
		t.Fatalf("往返失败：%v %v", got, err)
	}
}

func TestLoadBotRejectsBrokenJSON(t *testing.T) {
	botDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(botDir, "env.json"), []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBot(botDir); err == nil {
		t.Fatal("损坏的 env.json 应报错")
	}
}

func TestSaveRejectsInvalidVars(t *testing.T) {
	dir := t.TempDir()
	if err := SaveInstance(dir, map[string]string{"A-B": "1"}); err == nil {
		t.Fatal("非法名字不应写盘")
	}
	if err := SaveBot(dir, map[string]string{"A": "x\ny"}); err == nil {
		t.Fatal("非法值不应写盘")
	}
}

// 实例级 env 按行 trim，存不下首尾空白；不能静默丢，要显式报错并指向 bot 级 env.json。
func TestSaveInstanceRejectsWhitespaceEdges(t *testing.T) {
	dir := t.TempDir()
	for _, v := range []string{" x", "x ", " x ", " ", "\t"} {
		if err := SaveInstance(dir, map[string]string{"A": v}); err == nil {
			t.Fatalf("值 %q 的首尾空白无法往返，应报错", v)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "env")); !os.IsNotExist(err) {
		t.Fatalf("校验失败不应留下文件：%v", err)
	}
	if err := SaveBot(dir, map[string]string{"A": " x "}); err != nil {
		t.Fatalf("bot 级 env.json 应保留首尾空白：%v", err)
	}
	got, err := LoadBot(dir)
	if err != nil || got["A"] != " x " {
		t.Fatalf("bot 级往返失败：%v %v", got, err)
	}
}

// 调用级层：同名覆盖、新增变量、PATH 前置到低层 PATH 之前。
func TestOverlayCallLayer(t *testing.T) {
	base := []string{"PATH=/usr/bin:/bin", "A=base", "B=keep"}
	got, err := Overlay(base, map[string]string{"A": "call", "C": "new", "PATH": "/tmp/tools"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"PATH=/tmp/tools" + pathListSeparator() + "/usr/bin:/bin", "A=call", "B=keep", "C=new"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// 空值在调用级同样表示删除该键，不会被低层补回。
func TestOverlayEmptyValueDeletes(t *testing.T) {
	got, err := Overlay([]string{"A=base", "B=keep"}, map[string]string{"A": ""})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"B=keep"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// 没传调用级变量时原样返回，行为与引入 Overlay 之前一致。
func TestOverlayEmptyCallKeepsBase(t *testing.T) {
	base := []string{"A=base", "B=keep"}
	for _, call := range []map[string]string{nil, {}} {
		got, err := Overlay(base, call)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, base) {
			t.Fatalf("call=%v got %v want %v", call, got, base)
		}
	}
}

// 调用级与其他层用同一套校验：非法名字、密钥引用、换行都拒绝，并整体失败。
func TestOverlayRejectsInvalidEntries(t *testing.T) {
	base := []string{"A=base"}
	for _, c := range []struct{ name, value, hint string }{
		{"1BAD", "x", "环境变量名"},
		{"A-B", "x", "环境变量名"},
		{"OK", "{{secret:PG_PASS}}", "密钥请用设置页的密钥卡片"},
		{"OK", "a\nb", "不能含换行"},
		{"OK", "a\x00b", "不能含换行"},
	} {
		got, err := Overlay(base, map[string]string{"OK": "fine", c.name: c.value})
		if err == nil || !strings.Contains(err.Error(), c.hint) {
			t.Fatalf("%s=%q 应被拒绝，得到 %v", c.name, c.value, err)
		}
		if got != nil {
			t.Fatalf("%s=%q 被拒绝时不应返回环境列表：%v", c.name, c.value, got)
		}
	}
}
