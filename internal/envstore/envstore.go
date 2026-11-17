// Package envstore 管理普通环境变量的三层来源：进程环境（base）→ 实例级 → bot 级 → 单次调用。
//
// 文件：
//   - <实例目录>/env       每行 KEY=VALUE，`#` 开头为注释，权限 0600
//   - <bot 目录>/env.json  JSON 对象，权限 0600
//
// 实例级 env 文件按行 trim 解析，不保留值的首尾空白；需要首尾空白时用 bot 级 env.json。
//
// 调用级（只对一次命令生效）不落盘：shell 工具的 env 参数由 Overlay 在派发前
// 叠加到已合并的列表上，优先级最高。
//
// 本包只处理普通变量；密钥走 internal/vault，两者严格分开。
package envstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/kiry163/reeve/internal/atomicfile"
)

// Layer 标识变量的来源层，用于界面展示与排查。
type Layer string

const (
	LayerInstance Layer = "instance"
	LayerBot      Layer = "bot"
)

const (
	maxNameLen  = 128
	maxValueLen = 8192
)

var nameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidName 检查变量名：字母或下划线开头，只含字母、数字、下划线。
func ValidName(name string) error {
	if name == "" {
		return fmt.Errorf("环境变量名不能为空，起一个名字再试（例如 HTTPS_PROXY）")
	}
	if len(name) > maxNameLen {
		return fmt.Errorf("环境变量名太长（%d 字节）：最多 %d 个，换短一点的名字", len(name), maxNameLen)
	}
	if !nameRe.MatchString(name) {
		return fmt.Errorf("环境变量名 %q 不合法：要用字母或下划线开头，只含字母、数字和下划线（例如 HTTPS_PROXY）", name)
	}
	return nil
}

// ValidValue 检查变量值：不能含换行、回车或 NUL，长度不超过 8192 字节。
// 空值是合法的，在 Merge 里表示删除该键。
func ValidValue(v string) error {
	if strings.ContainsAny(v, "\n\r\x00") {
		return fmt.Errorf("环境变量的值不能含换行、回车或 NUL 字符，删掉这些字符再试")
	}
	if len(v) > maxValueLen {
		return fmt.Errorf("环境变量的值太长（%d 字节）：最多 %d 字节，改用文件配置", len(v), maxValueLen)
	}
	return nil
}

// ValidEntry 校验一次写入的变量名与值：名字、值按本包的规则校验，值另外不允许
// 出现 {{secret:…}} 引用——那是执行时的占位符，写进 env 只会变成字面量，密钥要走
// internal/vault 的密钥卡片。命令层、工具层、HTTP 层与调用级 env 都经过这里。
func ValidEntry(name, value string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if strings.Contains(value, "{{secret:") {
		return fmt.Errorf("环境变量 %s 的值不能引用密钥：密钥请用设置页的密钥卡片，不要写进环境变量", name)
	}
	if err := ValidValue(value); err != nil {
		return fmt.Errorf("环境变量 %s 的值不合法: %w", name, err)
	}
	return nil
}

// LoadInstance 读实例级变量（dir/env）。文件不存在时返回空 map。
// 每行去掉两端空格后解析，KEY 与 VALUE 各自去掉两端空格，值内部空格保留。
func LoadInstance(dir string) (map[string]string, error) {
	path := filepath.Join(dir, "env")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取实例级环境变量 %s 失败: %w，检查这个路径是否可读", path, err)
	}
	vars := map[string]string{}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("实例级环境变量 %s 第 %d 行不是 KEY=VALUE 格式，修正或删掉这一行", path, i+1)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if err := ValidName(key); err != nil {
			return nil, fmt.Errorf("实例级环境变量 %s 第 %d 行: %w", path, i+1, err)
		}
		if err := ValidValue(value); err != nil {
			return nil, fmt.Errorf("实例级环境变量 %s 第 %d 行（%s）: %w", path, i+1, key, err)
		}
		vars[key] = value
	}
	return vars, nil
}

// SaveInstance 排序写回实例级变量（dir/env，0600），目录不存在时创建。
func SaveInstance(dir string, vars map[string]string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建实例目录 %s 失败: %w，检查上级目录权限", dir, err)
	}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		if err := ValidName(k); err != nil {
			return fmt.Errorf("无法保存实例级变量: %w", err)
		}
		if err := ValidValue(vars[k]); err != nil {
			return fmt.Errorf("无法保存实例级变量 %s: %w", k, err)
		}
		if v := vars[k]; strings.TrimSpace(v) != v {
			return fmt.Errorf("无法保存实例级变量 %s: 值首尾有空白，实例级 env 文件不保留首尾空白；改用 bot 级 env.json 可以保留", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(vars[k])
		b.WriteByte('\n')
	}
	path := filepath.Join(dir, "env")
	if err := atomicfile.Write(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("写入实例级环境变量 %s 失败: %w，检查目录是否可写", path, err)
	}
	return nil
}

// LoadBot 读 bot 级变量（botDir/env.json）。文件不存在时返回空 map。
func LoadBot(botDir string) (map[string]string, error) {
	path := filepath.Join(botDir, "env.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 bot 级环境变量 %s 失败: %w，检查这个路径是否可读", path, err)
	}
	vars := map[string]string{}
	if err := json.Unmarshal(data, &vars); err != nil {
		return nil, fmt.Errorf("bot 级环境变量 %s 损坏: %w，修正为 {\"KEY\":\"VALUE\"} 格式", path, err)
	}
	for k, v := range vars {
		if err := ValidName(k); err != nil {
			return nil, fmt.Errorf("bot 级环境变量 %s: %w", path, err)
		}
		if err := ValidValue(v); err != nil {
			return nil, fmt.Errorf("bot 级环境变量 %s（%s）: %w", path, k, err)
		}
	}
	return vars, nil
}

// SaveBot 写 bot 级变量（botDir/env.json，0600），目录不存在时创建。
func SaveBot(botDir string, vars map[string]string) error {
	if err := os.MkdirAll(botDir, 0o700); err != nil {
		return fmt.Errorf("创建 bot 目录 %s 失败: %w，检查上级目录权限", botDir, err)
	}
	for k, v := range vars {
		if err := ValidName(k); err != nil {
			return fmt.Errorf("无法保存 bot 级变量: %w", err)
		}
		if err := ValidValue(v); err != nil {
			return fmt.Errorf("无法保存 bot 级变量 %s: %w", k, err)
		}
	}
	data, err := json.MarshalIndent(vars, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 bot 级环境变量失败: %w", err)
	}
	path := filepath.Join(botDir, "env.json")
	if err := atomicfile.Write(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("写入 bot 级环境变量 %s 失败: %w，检查目录是否可写", path, err)
	}
	return nil
}

// Merge 按 base → layer1 → layer2 … 合并，后者覆盖前者。
// 值为空字符串表示删除该键；PATH 特例为前置拼接（新值在前），
// Windows 上 Path/PATH 视为同一个键。
// 返回 KEY=VALUE 列表，base 的键保持原顺序，新增键按层内键名排序追加。
func Merge(base []string, layers ...map[string]string) []string {
	order := make([]string, 0, len(base))
	values := make(map[string]string, len(base))
	known := make(map[string]bool, len(base))

	for _, item := range base {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			continue
		}
		if !known[key] {
			known[key] = true
			order = append(order, key)
		}
		values[key] = value
	}

	for _, layer := range layers {
		keys := make([]string, 0, len(layer))
		for k := range layer {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := layer[k]
			if v == "" {
				unsetKey(values, k)
				continue
			}
			if isPathKey(k) {
				v = prependPath(values, k, v)
			}
			if !known[k] {
				known[k] = true
				order = append(order, k)
			}
			values[k] = v
		}
	}

	out := make([]string, 0, len(values))
	for _, k := range order {
		v, ok := values[k]
		if !ok {
			continue
		}
		out = append(out, k+"="+v)
	}
	return out
}

// Overlay 在已合并的环境列表 base 上应用调用层 call（只对本次命令生效的变量，
// 比如 shell 工具的 env 参数）。语义与 Merge 的其他层一致：同名覆盖，空值表示
// 删除该键，PATH 前置到低层 PATH 之前（callPATH + 分隔符 + basePATH）。
// 每个键先按 ValidEntry 校验，任一键不合法就整体返回错误，调用方不应执行命令。
func Overlay(base []string, call map[string]string) ([]string, error) {
	if len(call) == 0 {
		return base, nil
	}
	names := make([]string, 0, len(call))
	for k := range call {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if err := ValidEntry(k, call[k]); err != nil {
			return nil, fmt.Errorf("调用级环境变量: %w", err)
		}
	}
	return Merge(base, call), nil
}

// pathListSeparator 返回当前系统的 PATH 分隔符。
func pathListSeparator() string {
	if runtime.GOOS == "windows" {
		return ";"
	}
	return ":"
}

// isPathKey 判断是不是 PATH。Windows 的进程环境常把它写作 Path，且查找不区分
// 大小写，所以两种拼写必须当成同一个键。
func isPathKey(key string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(key, "PATH")
	}
	return key == "PATH"
}

// unsetKey 删除键；PATH 在 Windows 上不分大小写，任一拼写都删掉。
func unsetKey(values map[string]string, key string) {
	path := isPathKey(key)
	for existing := range values {
		if existing == key || (path && isPathKey(existing)) {
			delete(values, existing)
		}
	}
}

// prependPath 把 v 前置拼接到现有 PATH 上，并删掉其它拼写的旧键，
// 避免 Windows 子进程里出现两条 PATH、只剩最后一条。
func prependPath(values map[string]string, key, v string) string {
	olds := make([]string, 0, 2)
	for existing := range values {
		if existing != key && isPathKey(existing) {
			olds = append(olds, existing)
		}
	}
	// 极端情况下 base 里有多条不同拼写的 PATH，排序后拼接保证输出确定。
	sort.Strings(olds)
	parts := make([]string, 0, len(olds)+1)
	if old, ok := values[key]; ok && old != "" {
		parts = append(parts, old)
	}
	for _, existing := range olds {
		if old := values[existing]; old != "" {
			parts = append(parts, old)
		}
		delete(values, existing)
	}
	if len(parts) == 0 {
		return v
	}
	sep := pathListSeparator()
	return v + sep + strings.Join(parts, sep)
}
