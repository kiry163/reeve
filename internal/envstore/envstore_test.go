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
	got := Merge(base, map[string]string{"A": "instance", "C": "new"}, map[string]string{"C": "call", "B": ""})
	want := []string{"PATH=/usr/bin", "A=instance", "C=call"}
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

func TestLoadMissingFileGivesEmptyMap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nope")
	vars, err := LoadInstance(dir)
	if err != nil || vars == nil || len(vars) != 0 {
		t.Fatalf("缺失的实例级 env 应返回空 map 且无错误，得到 %v %v", vars, err)
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

func TestSaveRejectsInvalidVars(t *testing.T) {
	dir := t.TempDir()
	if err := SaveInstance(dir, map[string]string{"A-B": "1"}); err == nil {
		t.Fatal("非法名字不应写盘")
	}
	if err := SaveInstance(dir, map[string]string{"A": "x\ny"}); err == nil {
		t.Fatal("非法值不应写盘")
	}
}

// 实例级 env 按行 trim，存不下首尾空白；不能静默丢，要显式报错。
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

// 没传调用级变量时原样返回。
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

// 调用级与实例级用同一套校验：非法名字、密钥引用、换行都拒绝，并整体失败。
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

func TestShellEnvOnlyImportsAllowlistedKeys(t *testing.T) {
	t.Setenv("PATH", "/custom/bin:/usr/bin")
	t.Setenv("TZ", "UTC")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7890")
	t.Setenv("REEVE_TEST_SECRET", "must-not-import")
	got := ShellEnv()
	if got["PATH"] != "/custom/bin:/usr/bin" || got["TZ"] != "UTC" || got["HTTPS_PROXY"] != "http://127.0.0.1:7890" {
		t.Fatalf("允许列表内的变量应被读取：%v", got)
	}
	if _, ok := got["REEVE_TEST_SECRET"]; ok {
		t.Fatalf("非允许列表变量不应导入：%v", got)
	}
}

func TestRefreshInstancePreservesOtherKeys(t *testing.T) {
	dir := t.TempDir()
	if err := SaveInstance(dir, map[string]string{"CUSTOM": "keep", "PATH": "/old/bin"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/new/bin:/usr/bin")
	t.Setenv("TZ", "Asia/Shanghai")
	updates, err := RefreshInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	if updates["PATH"] != "/new/bin:/usr/bin" || updates["TZ"] != "Asia/Shanghai" {
		t.Fatalf("应返回导入的变量：%v", updates)
	}
	vars, err := LoadInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	if vars["CUSTOM"] != "keep" {
		t.Fatalf("手动添加的变量应保留：%v", vars)
	}
	if vars["PATH"] != "/new/bin:/usr/bin" || vars["TZ"] != "Asia/Shanghai" {
		t.Fatalf("允许列表变量应更新：%v", vars)
	}
}

func TestEnsureInstanceDoesNotOverwriteExisting(t *testing.T) {
	dir := t.TempDir()
	if err := SaveInstance(dir, map[string]string{"A": "1"}); err != nil {
		t.Fatal(err)
	}
	created, updates, err := EnsureInstance(dir)
	if err != nil || created || len(updates) != 0 {
		t.Fatalf("已有文件不应重新初始化：created=%v updates=%v err=%v", created, updates, err)
	}
	vars, _ := LoadInstance(dir)
	if !reflect.DeepEqual(vars, map[string]string{"A": "1"}) {
		t.Fatalf("已有文件被改动：%v", vars)
	}
}

func TestEnsureInstanceCreatesFromShell(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new")
	t.Setenv("PATH", "/shell/bin:/usr/bin")
	created, updates, err := EnsureInstance(dir)
	if err != nil || !created {
		t.Fatalf("应新建实例 env：created=%v err=%v", created, err)
	}
	if updates["PATH"] != "/shell/bin:/usr/bin" {
		t.Fatalf("应导入 PATH：%v", updates)
	}
	vars, err := LoadInstance(dir)
	if err != nil || vars["PATH"] != "/shell/bin:/usr/bin" {
		t.Fatalf("文件内容不对：%v %v", vars, err)
	}
}
