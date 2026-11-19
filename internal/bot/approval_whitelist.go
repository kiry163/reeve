package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// 实例级自动审批命令白名单：所有 Bot 共用，命中后 shell 命令跳过审批卡。
// 只支持整条命令精确匹配，或末尾 * 的前缀匹配。白名单不改变危险规则：
// rm -rf、sudo、git push 等仍然优先询问/拒绝；含 shell 控制符的复合命令也不会自动放行。

const (
	approvalWhitelistVersion = 1
	maxApprovalCommands      = 200
	maxApprovalCommandLen    = 512
)

type approvalWhitelistFile struct {
	Version  int      `json:"version"`
	Commands []string `json:"commands"`
}

// approvalWhitelistView 是网页设置页的读写形状。
type approvalWhitelistView struct {
	Commands []string `json:"commands"`
}

type approvalWhitelist struct {
	dir  string
	path string
	base []string // Config.Rules.AllowShell 的原始正则（当前没有外部入口，保留给测试/未来）

	mu       sync.RWMutex
	commands []string // 用户配置的原始命令模式
	regexes  []string // base + 由 commands 生成的锚定正则
}

// loadApprovalWhitelist 读取 <实例目录>/approval-whitelist.json。
// 文件不存在或损坏时以空白名单启动：配置读不懂时绝不自动放行命令。
func loadApprovalWhitelist(dir string, base []string) *approvalWhitelist {
	a := &approvalWhitelist{dir: dir, path: filepath.Join(dir, "approval-whitelist.json"), base: append([]string(nil), base...)}
	raw, err := os.ReadFile(a.path)
	if errors.Is(err, os.ErrNotExist) {
		a.regexes = a.withBase(nil)
		return a
	}
	if err != nil {
		log.Printf("读取 %s 失败：%v（自动审批白名单保持为空）", filepath.Base(a.path), err)
		a.regexes = a.withBase(nil)
		return a
	}
	var f approvalWhitelistFile
	if err := json.Unmarshal(raw, &f); err != nil {
		log.Printf("解析 %s 失败：%v（自动审批白名单保持为空）", filepath.Base(a.path), err)
		a.regexes = a.withBase(nil)
		return a
	}
	commands, patterns, err := compileApprovalCommands(f.Commands)
	if err != nil {
		log.Printf("%s 中的白名单无效：%v（自动审批白名单保持为空）", filepath.Base(a.path), err)
		a.regexes = a.withBase(nil)
		return a
	}
	a.commands, a.regexes = commands, a.withBase(patterns)
	return a
}

func (a *approvalWhitelist) view() approvalWhitelistView {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return approvalWhitelistView{Commands: append([]string{}, a.commands...)}
}

// rules 返回可直接交给 builtin.Guard 的正则列表（只读副本）。
func (a *approvalWhitelist) rules() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]string(nil), a.regexes...)
}

func (a *approvalWhitelist) withBase(patterns []string) []string {
	out := make([]string, 0, len(a.base)+len(patterns))
	out = append(out, a.base...)
	out = append(out, patterns...)
	return out
}

// save 校验、落盘并更新内存状态；返回新的正则列表，由调用方热更新各 Bot 的 Guard。
func (a *approvalWhitelist) save(commands []string) ([]string, error) {
	cleaned, patterns, err := compileApprovalCommands(commands)
	if err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(approvalWhitelistFile{Version: approvalWhitelistVersion, Commands: cleaned}, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writePrivate(a.dir, a.path, raw); err != nil {
		return nil, err
	}
	rules := a.withBase(patterns)
	a.mu.Lock()
	a.commands, a.regexes = cleaned, rules
	a.mu.Unlock()
	return append([]string(nil), rules...), nil
}

// compileApprovalCommands 校验并归一化白名单，同时生成锚定到整条命令的正则。
// 行尾的 * 表示前缀匹配；其他位置的 * 按字面量处理（会经过 QuoteMeta）。
func compileApprovalCommands(commands []string) (cleaned, patterns []string, err error) {
	nonEmpty := 0
	for _, raw := range commands {
		if strings.TrimSpace(raw) != "" {
			nonEmpty++
		}
	}
	if nonEmpty > maxApprovalCommands {
		return nil, nil, fmt.Errorf("最多保存 %d 条命令", maxApprovalCommands)
	}
	cleaned = make([]string, 0, nonEmpty)
	patterns = make([]string, 0, nonEmpty)
	seen := make(map[string]bool, nonEmpty)
	for i, raw := range commands {
		cmd := strings.TrimSpace(raw)
		if cmd == "" {
			continue
		}
		if err := validateApprovalCommand(cmd); err != nil {
			return nil, nil, fmt.Errorf("第 %d 条：%w", i+1, err)
		}
		if seen[cmd] {
			continue
		}
		seen[cmd] = true
		cleaned = append(cleaned, cmd)
		patterns = append(patterns, approvalCommandRegex(cmd))
	}
	return cleaned, patterns, nil
}

func validateApprovalCommand(cmd string) error {
	if len(cmd) > maxApprovalCommandLen {
		return fmt.Errorf("命令太长（最多 %d 字节）", maxApprovalCommandLen)
	}
	for _, r := range cmd {
		if r < 0x20 || r == 0x7f {
			return errors.New("命令不能包含控制字符")
		}
	}
	if strings.ContainsAny(cmd, ";&|<>`$") {
		return errors.New("白名单只支持单条简单命令，不能包含 ; & | < > $ 或反引号")
	}
	if cmd == "*" {
		return errors.New("不能只用 *，会放行所有命令")
	}
	return nil
}

// approvalCommandRegex 把用户写的简单模式转成锚定整条命令的正则。
// 其他字符全部 QuoteMeta，用户输入不会被当成正则元字符。
func approvalCommandRegex(cmd string) string {
	if strings.HasSuffix(cmd, "*") {
		return "^" + regexp.QuoteMeta(strings.TrimSuffix(cmd, "*")) + ".*$"
	}
	return "^" + regexp.QuoteMeta(cmd) + "$"
}
