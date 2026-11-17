package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kiry163/reeve/internal/envx"
	"github.com/kiry163/reeve/internal/exec"
)

// Status 是某个执行器 CLI 在这台机器上的安装情况（名称/登录方式来自 cliNames）。
type Status struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Installed    bool     `json:"installed"`
	Subscription string   `json:"subscription,omitempty"`
	Login        string   `json:"login,omitempty"`
	ModelOption  bool     `json:"model_option"`
	Models       []string `json:"models,omitempty"` // 常用模型（仅作建议，也可手填）
	Path         string   `json:"path,omitempty"`
	Managed      bool     `json:"managed,omitempty"` // 是 reeve 一键安装的（在 <data>/tools 里）
	// Version 是 `<exe> --version` 解析出的版本号，Available 表示它跑得起来；都不正常时 Note 说明原因。
	// Probed=false 表示这次没有真正探测（?probe=0 且无缓存、尚未安装），此时 Available 没有结论。
	Version   string `json:"version,omitempty"`
	Probed    bool   `json:"probed"`
	Available bool   `json:"available"`
	Note      string `json:"note,omitempty"`
	// Installable 表示这台机器可以在设置里一键安装/更新；Install 是最近一次安装的进度。
	Installable bool          `json:"installable"`
	Install     *InstallState `json:"install,omitempty"`
}

type cliInfo struct {
	name         string
	subscription string
	login        string
	modelOption  bool
	models       []string
}

// cliNames 是已知的 CLI 元信息，也是执行器的注册表：Name 为空 = 不支持这个 kind。
var cliNames = map[exec.Kind]cliInfo{
	exec.KindCodex: {name: "Codex CLI", subscription: "ChatGPT Plus / Pro / Business", login: "codex login", modelOption: true},
	exec.KindPi:    {name: "Pi", subscription: "用 reeve 的 API 渠道", modelOption: true},
	exec.KindOMP:   {name: "omp", subscription: "omp 自己的 provider / API Key", login: "omp（首次运行后配置 provider）", modelOption: true},
}

// detectKinds 是界面展示的执行器（顺序即界面顺序）。
var detectKinds = []exec.Kind{exec.KindCodex, exec.KindPi, exec.KindOMP}

// Name 返回执行器的显示名；未知 Kind 返回空串。
func Name(kind exec.Kind) string { return cliNames[kind].name }

// NewRegistry 注册 codex/pi/omp 三个执行器。
func NewRegistry() *exec.Registry { return exec.NewRegistry(NewCodex(), NewPi(), NewOMP()) }

// Detect 返回执行器的安装情况和版本。每个装了的 CLI 都会跑一次 `<exe> --version`
// （最多 5 秒，三个并行），所以调用方应当带上请求上下文。
// 探测结果按可执行文件路径缓存 10 秒，连点设置页不会反复起子进程。
func Detect(ctx context.Context) []Status { return detect(ctx, true) }

// DetectCached 是 `?probe=0` 用的轻量版：只重新解析安装状态（路径、一键安装进度），
// 版本与可用性取 10 秒内的缓存；没有缓存就留空，不启动任何子进程。
func DetectCached() []Status { return detect(context.Background(), false) }

func detect(ctx context.Context, probe bool) []Status {
	out := make([]Status, 0, len(detectKinds))
	prefixes := make([][]string, 0, len(detectKinds))
	managed := managedCLIBin()
	for _, kind := range detectKinds {
		info := cliNames[kind]
		id := string(kind)
		st := Status{ID: id, Name: info.name, Subscription: info.subscription,
			Login: info.login, ModelOption: info.modelOption, Models: info.models}
		var prefix []string
		if path, pre, err := resolve(kind); err == nil {
			st.Installed = true
			st.Path = path
			prefix = pre
			st.Managed = managed != "" && filepath.Dir(path) == managed
		} else {
			st.Note = fmt.Sprintf("找不到 %s，请先安装并放进 PATH", id)
		}
		st.Installable = installSupported(id) == nil
		st.Install = installSnapshot(id)
		out = append(out, st)
		prefixes = append(prefixes, prefix)
	}
	// 探测放在第二轮：先建好切片再并行写各自的下标，互不干扰。
	var wg sync.WaitGroup
	for i := range out {
		if !out[i].Installed {
			continue
		}
		if cached, ok := cachedProbe(probeKey(out[i].Path, prefixes[i])); ok {
			out[i].Version, out[i].Probed, out[i].Available, out[i].Note = cached.version, true, cached.available, cached.note
			continue
		}
		if !probe {
			continue // 没有缓存也不探测：Probed 保持 false，Available 不构成结论
		}
		wg.Add(1)
		go func(st *Status, prefix []string) {
			defer wg.Done()
			st.Version, st.Available, st.Note = probeAndCache(ctx, st.Path, prefix)
			// 请求中途被取消（切 Bot / 关页面）的探测结果不可信：不当成可用的结论。
			st.Probed = ctx.Err() == nil
		}(&out[i], prefixes[i])
	}
	wg.Wait()
	return out
}

// SetToolsDir 设置一键安装用的目录（<data>/tools，程序启动时调用一次）。
// 设置后 resolve 优先用 reeve 自己装的版本。
func SetToolsDir(dir string) {
	tools.Lock()
	tools.dir = dir
	tools.Unlock()
}

var tools struct {
	sync.RWMutex
	dir string
}

func toolsDir() string {
	tools.RLock()
	defer tools.RUnlock()
	return tools.dir
}

func managedCLIBin() string {
	if dir := toolsDir(); dir != "" {
		return filepath.Join(dir, "clis", "bin")
	}
	return ""
}

func managedNodeBin() string {
	if dir := toolsDir(); dir != "" {
		return filepath.Join(dir, "node", "bin")
	}
	return ""
}

// Lookup 按 Kind 找到 CLI 的可执行文件；prefix 是 Windows 下先于参数执行的入口脚本。
type Lookup func(kind exec.Kind) (exe string, prefix []string, err error)

var defaultLookup Lookup = resolve

func resolve(kind exec.Kind) (string, []string, error) {
	id := string(kind)
	if Name(kind) == "" {
		return "", nil, fmt.Errorf("不支持的 Agent CLI：%s", id)
	}
	if runtime.GOOS != "windows" {
		// reeve 一键安装的版本优先：在设置里点了「更新」，用的就应该是它。
		if m := managedCLIBin(); m != "" {
			if p := filepath.Join(m, id); isExecutable(p) {
				return p, nil, nil
			}
		}
		if path, err := osexec.LookPath(id); err == nil {
			return path, nil, nil
		}
		// 服务以 systemd 启动时 PATH 只有系统目录，不会加载 ~/.bashrc 或 nvm，
		// 而服务用户没有 root，CLI 通常只能装在自己的家目录里，所以再找一遍这些位置。
		for _, dir := range userBinDirs() {
			if p := filepath.Join(dir, id); isExecutable(p) {
				return p, nil, nil
			}
		}
		return "", nil, osexec.ErrNotFound
	}
	if path, err := osexec.LookPath(id + ".exe"); err == nil {
		return path, nil, nil
	}
	// npm installs .cmd shims on Windows. Launch the known package entry point
	// directly; cmd.exe would interpret shell characters in user prompts.
	shim, err := osexec.LookPath(id + ".cmd")
	if err != nil {
		return "", nil, err
	}
	root := filepath.Join(filepath.Dir(shim), "node_modules")
	var entries []string
	switch id {
	case "codex":
		entries = []string{"@openai/codex/bin/codex.js"}
	case "pi":
		entries = []string{"@earendil-works/pi-coding-agent/dist/bundle/cli.js", "@earendil-works/pi-coding-agent/dist/cli.js", "@mariozechner/pi-coding-agent/dist/cli.js"}
	}
	for _, entry := range entries {
		if p := filepath.Join(root, entry); isFile(p) {
			node := filepath.Join(filepath.Dir(shim), "node.exe")
			if !isFile(node) {
				node, err = osexec.LookPath("node.exe")
				if err != nil {
					return "", nil, err
				}
			}
			return node, []string{p}, nil
		}
	}
	return "", nil, errors.New("未找到 CLI 可执行文件")
}

// userBinDirs 是用户级安装常见的可执行文件目录（不需要 root）：~/.local/bin、npm 自定义前缀、
// nvm 装的各个 Node 版本（新版本在前）、Volta、Bun 等。
func userBinDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	dirs := []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, "bin"),
		filepath.Join(home, ".npm-global", "bin"),
	}
	if prefix := npmrcPrefix(filepath.Join(home, ".npmrc"), home); prefix != "" {
		dirs = append(dirs, filepath.Join(prefix, "bin"))
	}
	nvmDir := os.Getenv("NVM_DIR")
	if nvmDir == "" {
		nvmDir = filepath.Join(home, ".nvm")
	}
	dirs = append(dirs, nvmBinDirs(nvmDir)...)
	return append(dirs,
		filepath.Join(home, ".volta", "bin"),
		filepath.Join(home, ".bun", "bin"),
		filepath.Join(home, ".local", "share", "pnpm"),
	)
}

// npmrcPrefix 读出 ~/.npmrc 里的 prefix=（npm config set prefix 写的），没有就返回空。
func npmrcPrefix(path, home string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.TrimSpace(k) != "prefix" {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if strings.HasPrefix(v, "~/") {
			v = filepath.Join(home, v[2:])
		}
		if filepath.IsAbs(v) {
			return v
		}
	}
	return ""
}

// nvmBinDirs 列出 nvm 装的每个 Node 版本的 bin 目录，版本新的在前。
func nvmBinDirs(nvmDir string) []string {
	entries, err := os.ReadDir(filepath.Join(nvmDir, "versions", "node"))
	if err != nil {
		return nil
	}
	type ver struct {
		dir string
		n   [3]int
	}
	var vs []ver
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var v ver
		if _, err := fmt.Sscanf(e.Name(), "v%d.%d.%d", &v.n[0], &v.n[1], &v.n[2]); err != nil {
			continue
		}
		v.dir = filepath.Join(nvmDir, "versions", "node", e.Name(), "bin")
		vs = append(vs, v)
	}
	slices.SortFunc(vs, func(a, b ver) int {
		for i := range a.n {
			if a.n[i] != b.n[i] {
				return b.n[i] - a.n[i]
			}
		}
		return 0
	})
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.dir
	}
	return out
}

func isExecutable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// probeTimeout 是 `--version` 探测的硬上限：CLI 卡住也不能拖住启动或检测接口。
const probeTimeout = 5 * time.Second

// probeCacheTTL 是探测结果的复用窗口：设置页连点「重新检测」不必每次起子进程。
const probeCacheTTL = 10 * time.Second

type probeResult struct {
	version   string
	available bool
	note      string
	at        time.Time
}

var probeCache struct {
	sync.Mutex
	byKey map[string]probeResult
}

// probeKey 是缓存键：Windows 上 codex 与 pi 都解析到同一个 node.exe，靠 prefix（JS 入口）
// 区分，否则版本与失败原因会串台。
func probeKey(exe string, prefix []string) string {
	return exe + "\x00" + strings.Join(prefix, "\x00")
}

func cachedProbe(key string) (probeResult, bool) {
	probeCache.Lock()
	defer probeCache.Unlock()
	r, ok := probeCache.byKey[key]
	if !ok || time.Since(r.at) > probeCacheTTL {
		return probeResult{}, false
	}
	return r, true
}

func storeProbe(key, version string, available bool, note string) {
	probeCache.Lock()
	defer probeCache.Unlock()
	if probeCache.byKey == nil {
		probeCache.byKey = map[string]probeResult{}
	}
	probeCache.byKey[key] = probeResult{version: version, available: available, note: note, at: time.Now()}
}

// forgetProbe 丢掉某个 CLI 的探测缓存（一键安装成功后调用，避免 10 秒内还报旧版本）。
func forgetProbe(exe string, prefix []string) {
	probeCache.Lock()
	defer probeCache.Unlock()
	delete(probeCache.byKey, probeKey(exe, prefix))
}

// probeAndCache 探测一次并写入缓存。请求上下文已取消（前端切 Bot/卸载）时不写：
// 否则 abort 造成的假失败会占住缓存 10 秒，正常 CLI 一直显示「运行异常」。
func probeAndCache(ctx context.Context, exe string, prefix []string) (version string, available bool, note string) {
	v, err := probeVersion(ctx, exe, prefix)
	if err != nil {
		note = err.Error()
	}
	if ctx.Err() == nil {
		storeProbe(probeKey(exe, prefix), v, err == nil, note)
	}
	return v, err == nil, note
}

// versionPattern 匹配 `codex-cli 0.169.0` / `omp/18.11.0` / `omp v18.11.0` / 裸 `0.12.3`
// 里的版本号；预发布/构建后缀（-beta.1、+abc）一起带上。
var versionPattern = regexp.MustCompile(`(\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.]+)?)`)

// parseVersion 从 `--version` 的输出里取出版本号；没有版本号时返回空串。
func parseVersion(out string) string {
	if m := versionPattern.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// probeVersion 跑 `<exe> [prefix…] --version`（5 秒超时）并解析版本号。命令以非零退出
// 但输出了可解析的版本号时仍算可用（有些 CLI 会在 stderr 里顺带提示升级）。
// 环境用 specEnv：和真正执行时一致，npm 装的 `#!/usr/bin/env node` 才能找到 reeve 自带的 Node。
func probeVersion(ctx context.Context, exe string, prefix []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	args := append(append([]string{}, prefix...), "--version")
	cmd := osexec.CommandContext(ctx, exe, args...)
	cmd.Env = specEnv(exe, nil)
	out, err := cmd.CombinedOutput()
	name := filepath.Base(exe)
	if v := parseVersion(string(out)); v != "" {
		return v, nil
	}
	if text := strings.TrimSpace(string(out)); text != "" {
		return "", fmt.Errorf("%s --version 没有输出版本号，可能装坏了（输出：%s）", name, firstLine(text))
	}
	if err != nil {
		return "", fmt.Errorf("%s --version 失败（%v），可能装坏了", name, err)
	}
	return "", fmt.Errorf("%s --version 没有输出，可能装坏了", name)
}

// probeAvailable 是 Available 的薄包装：只关心跑不跑得起来，不关心版本号。
func probeAvailable(ctx context.Context, exe string, prefix []string) error {
	_, err := probeVersion(ctx, exe, prefix)
	return err
}

// CLI 认证方式：subscription（默认）只用 CLI 自己登录的官方账户，启动时去掉会让 CLI 改走按量计费的
// API Key 环境变量；env 原样保留 Reeve 进程的环境变量；channel 用 reeve 的 API 渠道（Pi 固定用这个）。
const (
	AuthSubscription = "subscription"
	AuthEnv          = "env"
	AuthChannel      = "channel"
)

// Options 是某个 Bot 给 CLI 的额外设置。JSON 与旧 builtin.CLIOptions 完全一致。
type Options struct {
	Model   string `json:"cli_model,omitempty"`
	Auth    string `json:"cli_auth,omitempty"`
	Channel string `json:"cli_channel,omitempty"` // AuthChannel 时用哪个 API 渠道
}

var (
	channelIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	cliModelPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@\[\]-]{0,99}$`)
)

// ValidateOptions 检查并规整 CLI 设置；不支持指定模型的 CLI 会忽略模型。
func ValidateOptions(kind exec.Kind, o Options) (Options, error) {
	o.Model, o.Channel = strings.TrimSpace(o.Model), strings.TrimSpace(o.Channel)
	switch o.Auth {
	case "":
		o.Auth = AuthSubscription
	case AuthSubscription, AuthEnv, AuthChannel:
	default:
		return Options{}, errors.New("CLI 认证方式无效")
	}
	if kind == "" {
		return Options{}, nil
	}
	if Name(kind) == "" {
		return Options{}, fmt.Errorf("不支持的 Agent CLI：%s", kind)
	}
	if kind == exec.KindPi {
		o.Auth = AuthChannel // Pi 只用 reeve 的 API 渠道
	}
	if o.Auth == AuthChannel {
		if o.Channel != "" && !channelIDPattern.MatchString(o.Channel) {
			return Options{}, errors.New("API 渠道 ID 无效")
		}
	} else {
		o.Channel = ""
	}
	if !cliNames[kind].modelOption {
		o.Model = ""
	}
	if o.Model != "" && !cliModelPattern.MatchString(o.Model) {
		return Options{}, errors.New("模型名只能包含字母、数字和 . _ - : / @ [ ]，且不能以符号开头")
	}
	return o, nil
}

// subscriptionEnv 是选择「官方订阅」时不传给 CLI 的环境变量：有这些变量时 CLI 会改用 API Key
// （按量计费）或第三方地址，而不是你登录的订阅账户。
var subscriptionEnv = map[exec.Kind][]string{
	exec.KindCodex: {"OPENAI_API_KEY", "OPENAI_BASE_URL", "CODEX_API_KEY"},
	exec.KindPi:    {"ANTHROPIC_API_KEY", "ANTHROPIC_OAUTH_TOKEN", "OPENAI_API_KEY", "GEMINI_API_KEY"},
}

// reeveOwnEnv 是 reeve 自己的凭据：任何情况下都不传给 CLI 子进程。
// 厂商 Key（OPENAI_API_KEY 等）不在这里——它们由 CommandEnv 按认证方式决定去留。
var reeveOwnEnv = envx.Credentials

// hiddenEnv 是默认不传给 CLI 的变量：reeve 自身凭据 + 会让 CLI 改走厂商 API Key 的变量。
var hiddenEnv = envx.Hidden

// safeEnviron 返回去掉了 reeve 自身密钥的进程环境。
func safeEnviron() []string { return envx.Filter(os.Environ(), hiddenEnv) }

// CommandEnv 返回 CLI 子进程的环境：任务环境非 nil 时以它为基座，
// 再按 CLI 认证方式过滤 reeve 自己的密钥。
//
// auth=env（沿用进程里的 API Key）时，SafeEnviron 已经剥掉了厂商 Key，
// 所以从进程环境补齐该 CLI 自己的厂商变量；基座里已有的同名键优先。
func CommandEnv(processEnv, envList []string, kind exec.Kind, auth string) []string {
	if envList == nil {
		// 继承进程环境时与 SafeEnviron 完全一致：reeve 自己的模型 Key / 主密钥
		// 一律不传，不依赖调用方先过滤。
		return envx.Filter(processEnv, hiddenEnv)
	}
	base := envList
	if auth == AuthEnv {
		base = withVendorEnv(base, processEnv, kind)
	}
	return cliEnv(base, kind, auth)
}

// withVendorEnv 从进程环境补齐 subscriptionEnv[kind] 列出的厂商变量（大小写不敏感），
// 只补 base 里没有的键。
func withVendorEnv(base, processEnv []string, kind exec.Kind) []string {
	have := make(map[string]bool, len(base))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		have[strings.ToUpper(k)] = true
	}
	out := slices.Clone(base)
	for _, want := range subscriptionEnv[kind] {
		if have[strings.ToUpper(want)] {
			continue
		}
		for _, kv := range processEnv {
			k, _, _ := strings.Cut(kv, "=")
			if strings.EqualFold(k, want) {
				out = append(out, kv)
				have[strings.ToUpper(want)] = true
				break
			}
		}
	}
	return out
}

// cliEnv keeps the CLI's own authentication but never passes Reeve's credentials.
// In subscription mode it also drops the vendor API-key variables, so the CLI uses
// the account logged in on this device.
func cliEnv(environ []string, kind exec.Kind, auth string) []string {
	drop := make(map[string]bool, len(hiddenEnv)+len(subscriptionEnv[kind]))
	for k := range hiddenEnv {
		drop[k] = true
	}
	for _, k := range subscriptionEnv[kind] {
		drop[strings.ToUpper(k)] = auth != AuthEnv
	}
	return envx.Filter(environ, drop)
}

// withoutEnv 去掉指定的环境变量（大小写不敏感）。
func withoutEnv(env []string, keys ...string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, d := range keys {
			if strings.EqualFold(k, d) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// withPathDir 把 dir 放到 PATH 最前面（已经在 PATH 里就不动）。npm 装的 CLI 用
// `#!/usr/bin/env node` 启动，node 和 CLI 在同一个 bin 目录，不在 PATH 里就跑不起来。
func withPathDir(env []string, dir string) []string {
	for i, kv := range env {
		key, val, _ := strings.Cut(kv, "=")
		if key != "PATH" {
			continue
		}
		if slices.Contains(filepath.SplitList(val), dir) {
			return env
		}
		out := slices.Clone(env)
		if val == "" {
			out[i] = "PATH=" + dir
		} else {
			out[i] = "PATH=" + dir + string(os.PathListSeparator) + val
		}
		return out
	}
	return append(slices.Clone(env), "PATH="+dir)
}
