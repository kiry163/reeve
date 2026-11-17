package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kiry163/reeve/internal/atomicfile"
	"github.com/kiry163/reeve/internal/exec"
	"github.com/kiry163/reeve/internal/exec/cli"
)

// Executors 是每个 Bot 的执行器配置：白名单 + 默认执行器 + 每个外部 CLI 的选项。
// 落盘到 <botDir>/executors.json（0600）。旧的 agent-connection.json 在首次读取时迁移。
type Executors struct {
	Allowed []string               `json:"allowed"`
	Default string                 `json:"default"`
	CLIs    map[string]cli.Options `json:"clis"`
}

func (b *Bot) executorsPath() string { return filepath.Join(b.cfg.DataDir, "executors.json") }

func defaultExecutors() Executors {
	return Executors{Allowed: []string{string(exec.KindBuiltin)}, Default: string(exec.KindBuiltin), CLIs: map[string]cli.Options{}}
}

// loadExecutors 读取权威配置；文件不存在时从旧 agent-connection.json 迁移一次。
func (b *Bot) loadExecutors() error {
	raw, err := os.ReadFile(b.executorsPath())
	if errors.Is(err, os.ErrNotExist) {
		return b.migrateAgentConnection()
	}
	if err != nil {
		return err
	}
	var in Executors
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("executors.json: %w", err)
	}
	out, dropped, err := b.sanitizeExecutors(in)
	if err != nil {
		return fmt.Errorf("executors.json: %w", err)
	}
	if len(dropped) > 0 {
		if err := b.writeExecutors(out); err != nil {
			return err
		}
	}
	b.modelMu.Lock()
	b.executors = out
	b.modelMu.Unlock()
	b.reportExecutorChanges(out, dropped)
	return nil
}

// migrateAgentConnection 把旧的「选一个 CLI」配置翻译成白名单 + 默认执行器，
// 写入 executors.json 后删除旧文件；之后只读新文件，二次启动是幂等的。
func (b *Bot) migrateAgentConnection() error {
	raw, err := os.ReadFile(b.connectionPath())
	if errors.Is(err, os.ErrNotExist) {
		b.modelMu.Lock()
		b.executors = defaultExecutors()
		b.modelMu.Unlock()
		return nil
	}
	if err != nil {
		return err
	}
	var c agentConnection
	if err := json.Unmarshal(raw, &c); err != nil {
		return fmt.Errorf("agent-connection.json: %w", err)
	}
	in := defaultExecutors()
	if id := strings.TrimSpace(c.CLI); id != "" {
		in.Allowed = append(in.Allowed, id)
		in.Default = id
		in.CLIs[id] = c.Options
	}
	out, dropped, err := b.sanitizeExecutors(in)
	if err != nil {
		return fmt.Errorf("agent-connection.json: %w", err)
	}
	if err := b.writeExecutors(out); err != nil {
		return err
	}
	if err := os.Remove(b.connectionPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	b.modelMu.Lock()
	b.executors = out
	b.modelMu.Unlock()
	b.reportExecutorChanges(out, dropped)
	return nil
}

// reportExecutorChanges 把启动时丢弃未知 kind 的结果写成事件：配置确实被改了，
// 前端能通过事件流看到；不用 notify，避免启动时叫醒聊天层。
func (b *Bot) reportExecutorChanges(out Executors, dropped []string) {
	if len(dropped) == 0 {
		return
	}
	b.store.Append("executors.updated", map[string]any{"allowed": out.Allowed, "default": out.Default}, false)
	b.store.Append("executors.dropped", map[string]any{"kinds": dropped}, false)
}

// Executors 返回当前配置的副本；调用方修改返回值不会影响 Bot。
func (b *Bot) Executors() Executors {
	b.modelMu.RLock()
	out := b.executors
	b.modelMu.RUnlock()
	out.Allowed = slices.Clone(out.Allowed)
	out.CLIs = maps.Clone(out.CLIs)
	return out
}

// SaveExecutors 整体替换配置并校验；白名单必须包含 builtin，Default 必须在 Allowed 内，
// 每个 kind 必须在注册表里（未注册的适配器不允许出现在白名单）。
func (b *Bot) SaveExecutors(in Executors) error {
	return b.updateExecutors(func(ex *Executors) error {
		*ex = in
		return nil
	})
}

// updateExecutors 在同一把锁里完成读-改-写-校验-落盘，避免并发入口互相覆盖。
func (b *Bot) updateExecutors(fn func(*Executors) error) error {
	b.modelMu.Lock()
	cur := b.executors
	cur.Allowed = slices.Clone(cur.Allowed)
	cur.CLIs = maps.Clone(cur.CLIs)
	if err := fn(&cur); err != nil {
		b.modelMu.Unlock()
		return err
	}
	out, err := b.cleanExecutors(cur)
	if err != nil {
		b.modelMu.Unlock()
		return err
	}
	if err := b.writeExecutors(out); err != nil {
		b.modelMu.Unlock()
		return err
	}
	b.executors = out
	b.modelMu.Unlock()
	// 白名单变了：重建聊天层工具，模型下一轮就能看到新的 executor enum（不必重启）。
	// 读侧走 b.registry()，这里整体替换指针，无需与聊天回合串行。
	b.rebuildChatTools()
	b.store.Append("executors.updated", map[string]any{"allowed": out.Allowed, "default": out.Default}, false)
	return nil
}

func (b *Bot) executorRegistered(kind string) bool {
	if kind == string(exec.KindBuiltin) {
		return true
	}
	_, ok := b.execs.Get(exec.Kind(kind))
	return ok
}

// sanitizeExecutors 是启动读取用的软降级：丢弃当前版本不认识的 kind，
// 补齐 builtin，并把默认值落到合法项上。它与 cleanExecutors 的严格校验区分开。
func (b *Bot) sanitizeExecutors(in Executors) (Executors, []string, error) {
	out := Executors{Default: strings.TrimSpace(in.Default), CLIs: map[string]cli.Options{}}
	seen := make(map[string]bool, len(in.Allowed))
	dropped := []string{}
	dropSeen := map[string]bool{}
	drop := func(kind string) {
		if kind != "" && !dropSeen[kind] {
			dropSeen[kind] = true
			dropped = append(dropped, kind)
		}
	}
	for _, kind := range in.Allowed {
		kind = strings.TrimSpace(kind)
		if kind == "" || seen[kind] {
			continue
		}
		if !b.executorRegistered(kind) {
			drop(kind)
			continue
		}
		seen[kind] = true
		out.Allowed = append(out.Allowed, kind)
	}
	if !seen[string(exec.KindBuiltin)] {
		out.Allowed = append([]string{string(exec.KindBuiltin)}, out.Allowed...)
		seen[string(exec.KindBuiltin)] = true
	}
	if out.Default == "" {
		out.Default = string(exec.KindBuiltin)
	}
	if !seen[out.Default] {
		if !b.executorRegistered(out.Default) {
			drop(out.Default)
		}
		out.Default = string(exec.KindBuiltin)
	}
	for kind, opts := range in.CLIs {
		kind = strings.TrimSpace(kind)
		if kind == "" || kind == string(exec.KindBuiltin) {
			continue
		}
		if !b.executorRegistered(kind) {
			drop(kind)
			continue
		}
		v, err := cli.ValidateOptions(exec.Kind(kind), opts)
		if err != nil {
			return Executors{}, nil, fmt.Errorf("执行器 %s 的设置无效：%w", kind, err)
		}
		out.CLIs[kind] = v
	}
	slices.Sort(dropped)
	return out, dropped, nil
}

func (b *Bot) cleanExecutors(in Executors) (Executors, error) {
	out := Executors{Default: strings.TrimSpace(in.Default), CLIs: map[string]cli.Options{}}
	seen := make(map[string]bool, len(in.Allowed))
	for _, kind := range in.Allowed {
		kind = strings.TrimSpace(kind)
		if kind == "" || seen[kind] {
			continue
		}
		if !b.executorRegistered(kind) {
			return Executors{}, fmt.Errorf("未知执行器 %q（可用：%s）", kind, strings.Join(b.executorKinds(), "、"))
		}
		seen[kind] = true
		out.Allowed = append(out.Allowed, kind)
	}
	if !seen[string(exec.KindBuiltin)] {
		return Executors{}, errors.New("白名单必须包含 builtin")
	}
	if out.Default == "" {
		out.Default = string(exec.KindBuiltin)
	}
	if !seen[out.Default] {
		return Executors{}, fmt.Errorf("默认执行器 %q 不在白名单里", out.Default)
	}
	for kind, opts := range in.CLIs {
		kind = strings.TrimSpace(kind)
		if kind == "" || kind == string(exec.KindBuiltin) {
			continue
		}
		if !b.executorRegistered(kind) {
			return Executors{}, fmt.Errorf("未知执行器 %q（可用：%s）", kind, strings.Join(b.executorKinds(), "、"))
		}
		v, err := cli.ValidateOptions(exec.Kind(kind), opts)
		if err != nil {
			return Executors{}, fmt.Errorf("执行器 %s 的设置无效：%w", kind, err)
		}
		out.CLIs[kind] = v
	}
	return out, nil
}

func (b *Bot) writeExecutors(ex Executors) error {
	raw, err := json.Marshal(ex)
	if err != nil {
		return err
	}
	return atomicfile.Write(b.executorsPath(), raw, 0o600)
}

// executorKinds 是白名单可用的全部值：builtin + 已注册的外部执行器。
func (b *Bot) executorKinds() []string {
	out := []string{string(exec.KindBuiltin)}
	for _, kind := range b.execs.Kinds() {
		out = append(out, string(kind))
	}
	return out
}

func (b *Bot) executorAllowed(kind string) bool {
	kind = strings.TrimSpace(kind)
	for _, allowed := range b.Executors().Allowed {
		if allowed == kind {
			return true
		}
	}
	return false
}

// DefaultExecutor 返回当前默认执行器（builtin 或已注册的外部 kind）。
func (b *Bot) DefaultExecutor() string { return b.Executors().Default }

// AllowedExecutors 返回白名单副本。
func (b *Bot) AllowedExecutors() []string { return b.Executors().Allowed }
