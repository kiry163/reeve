package envstore

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// RefreshKeys 是 `reeve service env refresh` 从当前用户 shell 导入的变量。
// 列表刻意保持很小：路径、语言/时区、代理与 TLS 证书设置能让后台服务在
// launchd / systemd 的最小环境里继续正常工作；密钥和任意变量不自动导入。
var RefreshKeys = []string{
	"PATH",
	"TZ",
	"LANG",
	"LC_ALL",
	"HTTP_PROXY",
	"HTTPS_PROXY",
	"NO_PROXY",
	"ALL_PROXY",
	"SSL_CERT_FILE",
	"SSL_CERT_DIR",
}

// ShellEnv 从当前进程环境取 RefreshKeys 中存在的变量。
func ShellEnv() map[string]string {
	updates := make(map[string]string, len(RefreshKeys))
	for _, key := range RefreshKeys {
		if value, ok := os.LookupEnv(key); ok {
			updates[key] = value
		}
	}
	return updates
}

// RefreshInstance 把当前 shell 的 RefreshKeys 写入实例 env 文件：
// 读取现有变量后只覆盖这些键，其他手动添加的变量保持不变。
func RefreshInstance(dir string) (map[string]string, error) {
	updates := ShellEnv()
	if len(updates) == 0 {
		return updates, nil
	}
	current, err := LoadInstance(dir)
	if err != nil {
		return nil, err
	}
	for key, value := range updates {
		current[key] = value
	}
	if err := SaveInstance(dir, current); err != nil {
		return nil, err
	}
	return updates, nil
}

// EnsureInstance 在实例 env 文件不存在时用当前 shell 初始化；已有文件不覆盖。
// created 表示这次新建了文件。
func EnsureInstance(dir string) (created bool, updates map[string]string, err error) {
	path := filepath.Join(dir, "env")
	if _, statErr := os.Stat(path); statErr == nil {
		return false, nil, nil
	} else if !os.IsNotExist(statErr) {
		return false, nil, fmt.Errorf("检查实例环境变量 %s 失败: %w", path, statErr)
	}
	updates, err = RefreshInstance(dir)
	if err != nil {
		return false, nil, err
	}
	if len(updates) == 0 {
		// 当前 shell 没有可导入的键，也建一个空文件，避免每次 install 都重复初始化。
		if err := SaveInstance(dir, map[string]string{}); err != nil {
			return false, nil, err
		}
	}
	return true, updates, nil
}

// SortedKeys 返回 map 的键排序结果，供 CLI 稳定输出。
func SortedKeys(vars map[string]string) []string {
	keys := make([]string, 0, len(vars))
	for key := range vars {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
