//go:build !windows

package envstore

import (
	"os"
	"testing"
)

// checkFileMode 断言文件权限；Windows 上权限位没有意义，在 perm_windows_test.go 里跳过。
func checkFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != want {
		t.Fatalf("%s 权限应 %v，得到 %v", path, want, got)
	}
}
