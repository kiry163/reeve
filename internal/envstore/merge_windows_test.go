//go:build windows

package envstore

import (
	"reflect"
	"testing"
)

// Windows 的进程环境把 PATH 写作 Path，两者必须当成一个键，否则子进程只看到 layer 的值。
func TestMergeWindowsPathCaseInsensitive(t *testing.T) {
	got := Merge([]string{`Path=C:\Windows`}, map[string]string{"PATH": `C:\bin`})
	want := []string{`PATH=C:\bin;C:\Windows`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Path/PATH 应合并为一条，得到 %v，期望 %v", got, want)
	}
}

func TestMergeWindowsPathUnsetAnySpelling(t *testing.T) {
	if got := Merge([]string{`Path=C:\Windows`}, map[string]string{"path": ""}); len(got) != 0 {
		t.Fatalf("任一拼写置空都应删除 PATH，得到 %v", got)
	}
}
