package bot

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/kiry163/reeve/internal/builtin"
	"github.com/kiry163/reeve/internal/envstore"
)

// instanceEnvDir 返回实例级 env 文件（<dir>/env）所在目录。
// 默认 Bot 的数据目录就是实例目录；非默认 Bot 的 TokenDir 指向主数据目录（M1 已保证）。
func (b *Bot) instanceEnvDir() string {
	if b.cfg.TokenDir != "" {
		return b.cfg.TokenDir
	}
	return b.cfg.DataDir
}

// redactEnvError 把 envstore 错误里的绝对路径换成 data/ 相对标签：
// 同一份文案会出现在命令结果、工具输出与 HTTP 响应里。
func (b *Bot) redactEnvError(err error) error {
	if err == nil {
		return nil
	}
	msg := b.redactDataDir(err.Error())
	if msg == err.Error() {
		return err
	}
	return errors.New(msg)
}

// InstanceEnv 读实例级环境变量。
func (b *Bot) InstanceEnv() (map[string]string, error) {
	vars, err := envstore.LoadInstance(b.instanceEnvDir())
	return vars, b.redactEnvError(err)
}

// BotEnv 读 bot 级环境变量（<DataDir>/env.json）。
func (b *Bot) BotEnv() (map[string]string, error) {
	vars, err := envstore.LoadBot(b.cfg.DataDir)
	return vars, b.redactEnvError(err)
}

// View 同时返回实例级和 bot 级环境变量，供界面与 list_env 展示。
func (b *Bot) View() (instance, bot map[string]string, err error) {
	if instance, err = b.InstanceEnv(); err != nil {
		return nil, nil, err
	}
	if bot, err = b.BotEnv(); err != nil {
		return nil, nil, err
	}
	return instance, bot, nil
}

// SetEnv 写变量：global=true 写实例级（所有 Bot 共享），否则写本 Bot 的 bot 级。
// 用户输入的命令直接生效（用户就是授权者），所以审计事件固定 source=user。
func (b *Bot) SetEnv(name, value string, global bool) error {
	if global {
		return b.SetInstanceEnv(name, value, "user")
	}
	return b.SetBotEnv(name, value, "user")
}

// UnsetEnv 删变量，global 语义同 SetEnv。
func (b *Bot) UnsetEnv(name string, global bool) error {
	if global {
		return b.UnsetInstanceEnv(name, "user")
	}
	return b.UnsetBotEnv(name, "user")
}

// SetBotEnv 写 bot 级变量：校验 → 落盘 → 写不含值的 env.set 事件。
// 值看起来像密钥时由工具层 / HTTP 层告警，这里只负责存取与审计。
func (b *Bot) SetBotEnv(name, value, source string) error {
	return b.redactEnvError(b.setEnvLayer(envstore.LayerBot, name, value, source))
}

// UnsetBotEnv 删 bot 级变量：校验 → 落盘 → 写 env.unset 事件。
func (b *Bot) UnsetBotEnv(name, source string) error {
	return b.redactEnvError(b.unsetEnvLayer(envstore.LayerBot, name, source))
}

// SetInstanceEnv 写实例级变量：校验 → 落盘（<实例目录>/env）→ 写 env.set 事件。
func (b *Bot) SetInstanceEnv(name, value, source string) error {
	return b.redactEnvError(b.setEnvLayer(envstore.LayerInstance, name, value, source))
}

// UnsetInstanceEnv 删实例级变量：校验 → 落盘 → 写 env.unset 事件。
func (b *Bot) UnsetInstanceEnv(name, source string) error {
	return b.redactEnvError(b.unsetEnvLayer(envstore.LayerInstance, name, source))
}

// ReplaceBotEnv 整体替换 bot 级变量：body 里没有的键会被删除。
// 先全量校验（任一键非法就一个键都不写），再在 envMu 内做 load → diff → 逐键变更，
// 与单键写入共用同一把锁，避免并发时基于过时快照覆盖别人的修改。
// 返回疑似密钥的告警（不拒绝保存）。
func (b *Bot) ReplaceBotEnv(vars map[string]string) ([]string, error) {
	warnings, err := b.replaceEnv(envstore.LayerBot, vars)
	return warnings, b.redactEnvError(err)
}

// ReplaceInstanceEnv 整体替换实例级变量，语义同 ReplaceBotEnv。
func (b *Bot) ReplaceInstanceEnv(vars map[string]string) ([]string, error) {
	warnings, err := b.replaceEnv(envstore.LayerInstance, vars)
	return warnings, b.redactEnvError(err)
}

func (b *Bot) replaceEnv(layer envstore.Layer, vars map[string]string) ([]string, error) {
	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	sort.Strings(names)
	// 先全量校验：错误信息带上具体变量名，避免写到一半才发现后面的键非法。
	var problems []string
	for _, name := range names {
		if err := validateEnv(name, vars[name]); err != nil {
			problems = append(problems, err.Error())
			continue
		}
		// SaveInstance 按行存，首尾空白会被 trim 掉、回读不一致，提前拒绝保证整体替换可回读。
		if layer == envstore.LayerInstance && strings.TrimSpace(vars[name]) != vars[name] {
			problems = append(problems, fmt.Sprintf("环境变量 %s 的值首尾有空白，实例级 env 文件不保留首尾空白", name))
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("环境变量校验失败: %s", strings.Join(problems, "；"))
	}

	b.envMu.Lock()
	defer b.envMu.Unlock()
	current, err := b.loadEnvLayer(layer)
	if err != nil {
		return nil, err
	}
	// 删除 body 里没有的键；再写入新增和变更的键。整个读-改-写都在 envMu 内，
	// 逐键调用不加锁的内部版本（审计事件只记名字）。
	removed := make([]string, 0, len(current))
	for name := range current {
		if _, ok := vars[name]; !ok {
			removed = append(removed, name)
		}
	}
	sort.Strings(removed)
	for _, name := range removed {
		if err := b.unsetEnvLayerLocked(layer, name, "user"); err != nil {
			return nil, err
		}
	}
	for _, name := range names {
		if old, ok := current[name]; ok && old == vars[name] {
			continue
		}
		if err := b.setEnvLayerLocked(layer, name, vars[name], "user"); err != nil {
			return nil, err
		}
	}
	warnings := []string{}
	for _, name := range names {
		if builtin.LooksLikeSecret(vars[name]) {
			warnings = append(warnings, fmt.Sprintf("%s: 值看起来像密钥，密钥请让用户在聊天里保存；env 的值会出现在模型上下文与界面里", name))
		}
	}
	return warnings, nil
}

func (b *Bot) setEnvLayer(layer envstore.Layer, name, value, source string) error {
	if err := validateEnv(name, value); err != nil {
		return err
	}
	b.envMu.Lock()
	defer b.envMu.Unlock()
	return b.setEnvLayerLocked(layer, name, value, source)
}

// setEnvLayerLocked 假定调用方已校验 name/value 并持有 envMu。
func (b *Bot) setEnvLayerLocked(layer envstore.Layer, name, value, source string) error {
	vars, err := b.loadEnvLayer(layer)
	if err != nil {
		return err
	}
	vars[name] = value
	if err := b.saveEnvLayer(layer, vars); err != nil {
		return fmt.Errorf("保存环境变量 %s 失败: %w", name, err)
	}
	b.appendEnvEvent("env.set", layer, name, source)
	return nil
}

func (b *Bot) unsetEnvLayer(layer envstore.Layer, name, source string) error {
	if err := envstore.ValidName(name); err != nil {
		return err
	}
	b.envMu.Lock()
	defer b.envMu.Unlock()
	return b.unsetEnvLayerLocked(layer, name, source)
}

// unsetEnvLayerLocked 假定调用方已校验 name 并持有 envMu。
func (b *Bot) unsetEnvLayerLocked(layer envstore.Layer, name, source string) error {
	vars, err := b.loadEnvLayer(layer)
	if err != nil {
		return err
	}
	if _, ok := vars[name]; !ok {
		return nil // 本来就不存在：不重写文件，也不写事件
	}
	delete(vars, name)
	if err := b.saveEnvLayer(layer, vars); err != nil {
		return fmt.Errorf("删除环境变量 %s 失败: %w", name, err)
	}
	b.appendEnvEvent("env.unset", layer, name, source)
	return nil
}

func (b *Bot) loadEnvLayer(layer envstore.Layer) (map[string]string, error) {
	if layer == envstore.LayerInstance {
		return envstore.LoadInstance(b.instanceEnvDir())
	}
	return envstore.LoadBot(b.cfg.DataDir)
}

func (b *Bot) saveEnvLayer(layer envstore.Layer, vars map[string]string) error {
	if layer == envstore.LayerInstance {
		return envstore.SaveInstance(b.instanceEnvDir(), vars)
	}
	return envstore.SaveBot(b.cfg.DataDir, vars)
}

// appendEnvEvent 记录审计事件：只写层、变量名和来源，绝不写值。
func (b *Bot) appendEnvEvent(kind string, layer envstore.Layer, name, source string) {
	b.store.Append(kind, map[string]string{
		"layer": string(layer), "name": name, "source": source,
	}, false)
}

// TaskEnv 合并进程环境 → 实例级 → bot 级，返回 KEY=VALUE 列表。
// 调用级（单次命令）由 shell 工具的 env 参数在 exec 前用 envstore.Overlay 叠加。
// base 用 SafeEnviron 而不是 os.Environ：执行器 shell 会直接用它，reeve 自己的
// 模型 Key / 主密钥不能泄漏给模型；用户显式写进实例级或 bot 级的值仍会保留。
// 读文件失败时跳过那一层并记日志：派活不应该因为一个坏掉的 env 文件完全失败。
func (b *Bot) TaskEnv() []string {
	instance, err := b.InstanceEnv()
	if err != nil {
		log.Printf("读取实例级环境变量失败，本次派活只用 bot 级: %v", err)
		instance = nil
	}
	bot, err := b.BotEnv()
	if err != nil {
		log.Printf("读取 bot 级环境变量失败，本次派活只用实例级: %v", err)
		bot = nil
	}
	return envstore.Merge(builtin.SafeEnviron(), instance, bot)
}

func validateEnv(name, value string) error {
	return envstore.ValidEntry(name, value)
}
