// Package commands 定义斜线命令的注册表与解析规则。
//
// 命令由名字、别名、说明、用法与处理函数组成，供 /help 与网页命令面板
// 共用一套定义。解析后的参数通过 Input 传给处理函数，处理函数只依赖
// BotAPI，不感知聊天层。
package commands

import (
	"context"
	"fmt"
	"strings"
)

// Definition 是一条斜线命令的完整定义。
type Definition struct {
	Name    string
	Aliases []string
	Summary string
	Usage   string
	Run     func(ctx context.Context, in Input) Result
}

// Registry 按注册顺序保存命令定义，并按名字或别名查找。
type Registry struct {
	defs   []Definition
	byName map[string]int
}

// NewRegistry 返回空注册表。
func NewRegistry() *Registry {
	return &Registry{byName: make(map[string]int)}
}

// register 追加一条命令定义。名字与别名会统一转成小写，重复注册会 panic，
// 避免别名被静默覆盖。
func (r *Registry) register(d Definition) {
	if d.Name == "" {
		panic("commands: 命令名不能为空")
	}
	names := append([]string{d.Name}, d.Aliases...)
	for _, name := range names {
		key := strings.ToLower(name)
		if key == "" {
			panic("commands: 别名不能为空")
		}
		if _, ok := r.byName[key]; ok {
			panic(fmt.Sprintf("commands: 命令名或别名重复：%s", name))
		}
	}
	for _, name := range names {
		r.byName[strings.ToLower(name)] = len(r.defs)
	}
	r.defs = append(r.defs, d)
}

// Lookup 按名字或别名查找命令，名字大小写不敏感。
func (r *Registry) Lookup(name string) (Definition, bool) {
	i, ok := r.byName[strings.ToLower(name)]
	if !ok {
		return Definition{}, false
	}
	d := r.defs[i]
	if len(d.Aliases) > 0 {
		d.Aliases = append([]string(nil), d.Aliases...)
	}
	return d, true
}

// Definitions 按注册顺序返回命令定义的副本。调用方修改返回值不会影响注册表。
func (r *Registry) Definitions() []Definition {
	out := make([]Definition, len(r.defs))
	for i, d := range r.defs {
		out[i] = d
		if len(d.Aliases) > 0 {
			out[i].Aliases = append([]string(nil), d.Aliases...)
		}
	}
	return out
}

// help 生成帮助文本：每条命令一行 `/name  —  summary`，用法单独一行。
func (r *Registry) help() string {
	var b strings.Builder
	for _, d := range r.defs {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "/%s  —  %s", d.Name, d.Summary)
		if d.Usage != "" {
			b.WriteString("\n  ")
			b.WriteString(d.Usage)
		}
	}
	return b.String()
}
