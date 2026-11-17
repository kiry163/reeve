//go:build windows

package envstore

import (
	"os"
	"testing"
)

// checkFileMode 在 Windows 上只确认文件存在：权限位不影响访问控制。
func checkFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
