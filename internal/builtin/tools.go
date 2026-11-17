package builtin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kiry163/reeve/internal/envstore"
	"github.com/kiry163/reeve/internal/envx"
	"github.com/kiry163/reeve/internal/skills"
)

// Tool 是一个工具。Risk 根据参数给出风险等级，审批在 Execute 之前进行。
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	Risk(args json.RawMessage) Risk
	Execute(ctx context.Context, env ToolEnv, args json.RawMessage) (string, error)
}

// ToolEnv 是执行工具时的环境。
type ToolEnv struct {
	Workspace string
	TaskID    string
	Env       map[string]string // 额外环境变量（shell 引用密钥时由执行层注入）
	EnvList   []string          // 任务级合并后的完整环境；nil 时 shell 回退继承进程环境
	EnvStore  EnvStore          // 环境变量读写（list_env/set_env/unset_env）；nil 表示执行环境不支持
	Skills    *skills.Set       // read_skill 读取的技能集合；nil 表示没有技能
}

// FuncTool 用几个函数快速定义一个工具，自己加工具时推荐用它。
type FuncTool struct {
	ToolName, Desc, JSONSchema string
	RiskFn                     func(args json.RawMessage) Risk
	Fn                         func(ctx context.Context, env ToolEnv, args json.RawMessage) (string, error)
}

func (f FuncTool) Name() string            { return f.ToolName }
func (f FuncTool) Description() string     { return f.Desc }
func (f FuncTool) Schema() json.RawMessage { return json.RawMessage(f.JSONSchema) }
func (f FuncTool) Risk(a json.RawMessage) Risk {
	if f.RiskFn == nil {
		return RiskExec
	}
	return f.RiskFn(a)
}
func (f FuncTool) Execute(ctx context.Context, env ToolEnv, a json.RawMessage) (string, error) {
	return f.Fn(ctx, env, a)
}

// Registry 工具注册表。
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string
}

func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{tools: map[string]Tool{}}
	for _, t := range tools {
		r.Register(t)
	}
	return r
}

func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[t.Name()]; !ok {
		r.order = append(r.order, t.Name())
	}
	r.tools[t.Name()] = t
}

func (r *Registry) Get(name string) Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tools[name]
}

func (r *Registry) Specs() []ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []ToolSpec
	for _, n := range r.order {
		t := r.tools[n]
		out = append(out, ToolSpec{t.Name(), t.Description(), t.Schema()})
	}
	return out
}

// ResolvePath 把路径解析到工作区内（会跟随符号链接）；逃出工作区就报错。
func ResolvePath(workspace, p string) (string, error) {
	root, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	if realRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = realRoot
	}
	abs := p
	if !filepath.IsAbs(p) {
		abs = filepath.Join(root, p)
	}
	abs = filepath.Clean(abs)
	// 找到最近一个存在的上级目录，解析它的符号链接，再拼回剩下的部分
	real, rest := abs, ""
	for {
		if r, err := filepath.EvalSymlinks(real); err == nil {
			real = filepath.Join(r, rest)
			break
		}
		parent := filepath.Dir(real)
		if parent == real {
			break
		}
		rest = filepath.Join(filepath.Base(real), rest)
		real = parent
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("路径 %q 超出了工作区 %s", p, root)
	}
	return real, nil
}

func parseArgs[T any](raw json.RawMessage) (T, error) {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("参数格式错误: %w", err)
	}
	return v, nil
}

func readOnly(json.RawMessage) Risk { return RiskRead }
func writes(json.RawMessage) Risk   { return RiskWrite }

// DefaultTools 返回内置的基础工具。
func DefaultTools() []Tool {
	return []Tool{readFileTool, writeFileTool, editFileTool, listDirTool, grepTool, ShellTool, listEnvTool, setEnvTool, unsetEnvTool, readSkillTool}
}

var readFileTool = FuncTool{"read_file", "读取工作区内的文本文件。可用 offset(从1开始的行号)和 limit(行数)读取大文件的一部分。",
	`{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer"},"limit":{"type":"integer"}},"required":["path"]}`,
	readOnly, func(ctx context.Context, env ToolEnv, raw json.RawMessage) (string, error) {
		a, err := parseArgs[struct {
			Path          string
			Offset, Limit int
		}](raw)
		if err != nil {
			return "", err
		}
		p, err := ResolvePath(env.Workspace, a.Path)
		if err != nil {
			return "", err
		}
		st, err := os.Stat(p)
		if err != nil {
			return "", err
		}
		if st.IsDir() {
			return "", fmt.Errorf("%s 是目录，请用 list_dir", a.Path)
		}
		if st.Size() > maxReadFileBytes {
			switch {
			case a.Offset <= 0 && a.Limit <= 0:
				return "", fmt.Errorf("文件有 %d 字节，超过 10 MiB；请用 offset/limit 分段读取", st.Size())
			case a.Limit <= 0:
				return "", fmt.Errorf("文件有 %d 字节，超过 10 MiB；按 offset 分段读时也必须给 limit（例如 offset=%d&limit=200）", st.Size(), max(a.Offset, 1))
			}
		}
		if a.Offset <= 0 && a.Limit <= 0 {
			data, err := os.ReadFile(p)
			if err != nil {
				return "", err
			}
			return string(data), nil
		}
		if st.Size() > maxReadFileBytes {
			return readLineRange(p, a.Offset, a.Limit)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		lines := strings.Split(string(data), "\n")
		start := max(a.Offset-1, 0)
		end := len(lines)
		if a.Limit > 0 && start+a.Limit < end {
			end = start + a.Limit
		}
		if start >= len(lines) {
			return "", fmt.Errorf("offset 超出文件行数 %d", len(lines))
		}
		return strings.Join(lines[start:end], "\n"), nil
	}}

// maxReadFileBytes 是 read_file 一次整体读取的上限；更大的文件必须用 offset/limit。
const maxReadFileBytes = 10 << 20

// readLineRange 只读指定行范围，用于超过 10 MiB 的文件（不会把整个文件读进内存）。
func readLineRange(p string, offset, limit int) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	var out []string
	n, eof := 0, true
	for sc.Scan() {
		n++
		if n >= offset {
			out = append(out, sc.Text())
			if limit > 0 && len(out) >= limit {
				eof = false // 提前停止，不需要知道总行数
				break
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if eof && offset > n {
		return "", fmt.Errorf("offset 超出文件行数 %d", n)
	}
	return strings.Join(out, "\n"), nil
}

var writeFileTool = FuncTool{"write_file", "写入（覆盖）工作区内的文件，会自动创建上级目录。",
	`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`,
	writes, func(ctx context.Context, env ToolEnv, raw json.RawMessage) (string, error) {
		a, err := parseArgs[struct{ Path, Content string }](raw)
		if err != nil {
			return "", err
		}
		p, err := ResolvePath(env.Workspace, a.Path)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, []byte(a.Content), 0o644); err != nil {
			return "", err
		}
		return fmt.Sprintf("已写入 %s（%d 字节）", a.Path, len(a.Content)), nil
	}}

var editFileTool = FuncTool{"edit_file", "精确替换文件中的一段文字。old_string 必须在文件中唯一出现，除非 replace_all=true。",
	`{"type":"object","properties":{"path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"},"replace_all":{"type":"boolean"}},"required":["path","old_string","new_string"]}`,
	writes, func(ctx context.Context, env ToolEnv, raw json.RawMessage) (string, error) {
		a, err := parseArgs[struct {
			Path       string
			OldString  string `json:"old_string"`
			NewString  string `json:"new_string"`
			ReplaceAll bool   `json:"replace_all"`
		}](raw)
		if err != nil {
			return "", err
		}
		p, err := ResolvePath(env.Workspace, a.Path)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		n := strings.Count(string(data), a.OldString)
		switch {
		case a.OldString == "" || n == 0:
			return "", errors.New("old_string 在文件中没有找到")
		case n > 1 && !a.ReplaceAll:
			return "", fmt.Errorf("old_string 出现了 %d 次，请提供更多上下文让它唯一，或设置 replace_all", n)
		}
		out := strings.Replace(string(data), a.OldString, a.NewString, map[bool]int{true: -1, false: 1}[a.ReplaceAll])
		if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
			return "", err
		}
		return fmt.Sprintf("已修改 %s（替换 %d 处）", a.Path, map[bool]int{true: n, false: 1}[a.ReplaceAll]), nil
	}}

var listDirTool = FuncTool{"list_dir", "列出目录内容，目录名以 / 结尾。",
	`{"type":"object","properties":{"path":{"type":"string","description":"默认为工作区根目录"}}}`,
	readOnly, func(ctx context.Context, env ToolEnv, raw json.RawMessage) (string, error) {
		a, _ := parseArgs[struct{ Path string }](raw)
		p, err := ResolvePath(env.Workspace, a.Path)
		if err != nil {
			return "", err
		}
		ents, err := os.ReadDir(p)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for _, e := range ents {
			if e.IsDir() {
				fmt.Fprintf(&b, "%s/\n", e.Name())
			} else if info, err := e.Info(); err == nil {
				fmt.Fprintf(&b, "%s  (%d B)\n", e.Name(), info.Size())
			}
		}
		if b.Len() == 0 {
			return "(空目录)", nil
		}
		return b.String(), nil
	}}

var grepTool = FuncTool{"grep", "在工作区里用正则搜索文件内容，返回 文件:行号: 内容（最多 200 条）。",
	`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"}},"required":["pattern"]}`,
	readOnly, func(ctx context.Context, env ToolEnv, raw json.RawMessage) (string, error) {
		a, err := parseArgs[struct{ Pattern, Path string }](raw)
		if err != nil {
			return "", err
		}
		re, err := regexp.Compile(a.Pattern)
		if err != nil {
			return "", err
		}
		root, err := ResolvePath(env.Workspace, a.Path)
		if err != nil {
			return "", err
		}
		var hits []string
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil || len(hits) >= 200 {
				return filepath.SkipAll
			}
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if n := d.Name(); n == ".git" || n == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			// WalkDir does not descend into symlink directories, but ReadFile would
			// follow symlink files. Skip links (and devices/FIFOs) entirely.
			if !d.Type().IsRegular() {
				return nil
			}
			p, err = ResolvePath(env.Workspace, p)
			if err != nil {
				return nil
			}
			if info, err := d.Info(); err != nil || info.Size() > 2<<20 {
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil || bytes.IndexByte(data[:min(len(data), 512)], 0) >= 0 {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			sc := bufio.NewScanner(bytes.NewReader(data))
			sc.Buffer(nil, 1<<20)
			for i := 1; sc.Scan() && len(hits) < 200; i++ {
				if re.MatchString(sc.Text()) {
					hits = append(hits, fmt.Sprintf("%s:%d: %s", rel, i, sc.Text()))
				}
			}
			return nil
		})
		sort.Strings(hits)
		if len(hits) == 0 {
			return "没有匹配", nil
		}
		return strings.Join(hits, "\n"), nil
	}}

var literalEcho = regexp.MustCompile(`^echo( [a-zA-Z0-9_,.!:=@%+ -]*)?$`)
var literalLS = regexp.MustCompile(`^ls( -[alh]+)*$`)

// Shell syntax, command options, git helpers and symlinks make general shell
// commands unsafe to classify by prefix. File reads use the bounded file tools;
// only these literal, side-effect-free forms bypass command approval.
func ShellRisk(cmd string) Risk {
	if cmd == "pwd" || literalEcho.MatchString(cmd) || literalLS.MatchString(cmd) {
		return RiskRead
	}
	return RiskExec
}

// ShellTool 用 bash -c 在工作区目录里执行命令；超时或取消会杀掉整个进程组。
// env 参数是调用级环境变量：只对这一条命令生效，不落盘、不影响后续任务。
var ShellTool = FuncTool{"shell", "在工作区目录中用 bash 执行命令，返回输出和退出码。timeout_sec 默认 120，最大 600。",
	`{"type":"object","properties":{"command":{"type":"string"},"timeout_sec":{"type":"integer"},"env":{"type":"object","additionalProperties":{"type":"string"},"description":"仅本次命令生效的环境变量（KEY: VALUE），不保存、不影响后续任务；PATH 会前置到现有 PATH 之前"}},"required":["command"]}`,
	func(raw json.RawMessage) Risk {
		a, _ := parseArgs[struct{ Command string }](raw)
		return ShellRisk(a.Command)
	},
	func(ctx context.Context, env ToolEnv, raw json.RawMessage) (string, error) {
		a, err := parseArgs[struct {
			Command    string
			TimeoutSec int `json:"timeout_sec"`
			Env        map[string]string
		}](raw)
		if err != nil {
			return "", err
		}
		base := env.EnvList
		if base == nil {
			base = SafeEnviron()
		}
		// 调用级 env 先校验再合并；不合法时直接回工具错误，不执行命令。
		cmdEnv, err := envstore.Overlay(base, a.Env)
		if err != nil {
			return "", err
		}
		to := time.Duration(a.TimeoutSec) * time.Second
		if to <= 0 {
			to = 120 * time.Second
		}
		ctx, cancel := context.WithTimeout(ctx, min(to, 600*time.Second))
		defer cancel()
		cmd := shellCommand(ctx, a.Command)
		cmd.Dir = env.Workspace
		cmd.Env = cmdEnv
		for k, v := range env.Env { // 密钥注入，覆盖同名项
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		out := &cappedBuffer{max: shellMaxOutput}
		cmd.Stdout, cmd.Stderr = out, out
		setProcessGroup(cmd)
		cmd.WaitDelay = 2 * time.Second
		err = cmd.Run()
		res := out.String()
		if out.truncated {
			res += "\n[输出超过 1 MiB，已截断]"
		}
		if ctx.Err() != nil {
			return res, fmt.Errorf("命令被终止: %v", ctx.Err())
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Sprintf("%s\n[exit code %d]", res, ee.ExitCode()), nil
		}
		if err != nil {
			return res, err
		}
		return res + "\n[exit code 0]", nil
	}}

// shellMaxOutput 是单次命令输出（stdout+stderr）的上限；超出部分丢弃并提示。
const shellMaxOutput = 1 << 20

// cappedBuffer 累积到上限就丢弃后面的输出，但总是向命令报告写成功（避免命令因 EPIPE 提前退出）。
type cappedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room < len(p) {
		if room > 0 {
			c.buf.Write(p[:room])
		}
		c.truncated = true
	} else {
		c.buf.Write(p)
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string { return c.buf.String() }

// SafeEnviron 返回去掉了 reeve 自身凭据与厂商 Key 的环境变量（清单见 internal/envx）。
func SafeEnviron() []string { return envx.Filter(os.Environ(), envx.Hidden) }

// shellCommand 优先用 bash；Windows 上没有 bash（没装 Git Bash / WSL）时退回 PowerShell。
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("bash"); err != nil {
			return exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", command)
		}
	}
	return exec.CommandContext(ctx, "bash", "-c", command)
}
