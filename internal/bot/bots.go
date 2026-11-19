package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/kiry163/reeve/internal/builtin"
	"github.com/kiry163/reeve/internal/exec"
	"github.com/kiry163/reeve/internal/exec/cli"
)

const defaultBotID = "main"

// 最多 32 个 Bot：每个 Bot 都有自己的聊天循环、事件日志和后台任务队列，再多对本机/小服务器没有意义。
// 头像库有 224 个位置，新 Bot 取最小的空位。
const (
	maxBots     = 32
	avatarSlots = 224
)

type BotProfile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Instructions string `json:"instructions"`
	AvatarSlot   int    `json:"avatarSlot"`
}

func (b *Bot) botProfile() BotProfile {
	if p := b.profile.Load(); p != nil {
		return *p
	}
	return BotProfile{ID: defaultBotID, Name: b.cfg.Name}
}

// Bots owns one runtime per profile. Keep the original runtime and its paths
// for the default Bot so existing data and API clients keep working.
type Bots struct {
	mu       sync.RWMutex
	main     *Bot
	workRoot string
	profiles []BotProfile
	runtimes map[string]*Bot
	handlers map[string]*botEndpoint
	retired  sync.WaitGroup
	sharedMu sync.Mutex
	closed   bool
	gateway  *cliGateway        // 给 Pi 等外部 CLI 用的本机模型网关
	push     *pushManager       // 实例级 ntfy 推送
	approval *approvalWhitelist // 实例级自动审批命令白名单

	approvalMu sync.Mutex // 串行化白名单保存：落盘、更新内存、热应用到各 Bot 必须是一个整体
}

type botEndpoint struct {
	http.Handler
	requests sync.WaitGroup
}

var botIDPattern = regexp.MustCompile(`^bot_[a-f0-9]{16}$`)

func OpenBots(cfg Config) (*Bots, error) {
	workspace, err := filepath.Abs(cfg.Workspace)
	if err != nil {
		return nil, err
	}
	cli.SetToolsDir(filepath.Join(cfg.DataDir, "tools"))
	// 自动审批白名单是实例级的：先加载，再让默认 Bot 和后续 Bot 都用同一份。
	approval := loadApprovalWhitelist(cfg.DataDir, cfg.Rules.AllowShell)
	cfg.Rules.AllowShell = approval.rules()
	main, err := New(cfg)
	if err != nil {
		return nil, err
	}
	bs := &Bots{main: main, workRoot: workspace + "-bots", profiles: []BotProfile{main.botProfile()},
		runtimes: map[string]*Bot{defaultBotID: main}, handlers: map[string]*botEndpoint{defaultBotID: {Handler: main.Handler("")}},
		approval: approval}
	main.coordinator = bs
	if bs.gateway, err = startCLIGateway(main.pool, main.redactor); err != nil {
		bs.Close()
		return nil, err
	}
	cli.SetGateway(bs.gateway)
	raw, err := os.ReadFile(bs.path())
	if err == nil {
		err = json.Unmarshal(raw, &bs.profiles)
		if err == nil {
			err = validateProfiles(bs.profiles)
		}
	} else if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err != nil {
		bs.Close()
		return nil, fmt.Errorf("读取 Bot 列表: %w", err)
	}
	for _, p := range bs.profiles {
		if p.ID == defaultBotID {
			main.profile.Store(&p)
			continue
		}
		b, err := bs.open(p)
		if err != nil {
			bs.Close()
			return nil, fmt.Errorf("打开 Bot %s: %w", p.ID, err)
		}
		bs.runtimes[p.ID], bs.handlers[p.ID] = b, &botEndpoint{Handler: b.Handler("")}
		b.coordinator = bs
	}
	// 推送在 Start 之前挂上：只推新事件，启动重放和 recover 产生的历史事件不打扰用户。
	bs.push = newPushManager(main.cfg.DataDir, main.vault)
	for _, b := range bs.runtimes {
		bs.push.attach(b)
	}
	for _, b := range bs.runtimes {
		b.Start()
	}
	return bs, nil
}

func validateProfiles(profiles []BotProfile) error {
	if len(profiles) == 0 || len(profiles) > maxBots || profiles[0].ID != defaultBotID {
		return errors.New("Bot 列表缺少默认 Bot 或数量超限")
	}
	ids, slots := map[string]bool{}, map[int]bool{}
	for _, p := range profiles {
		if ids[p.ID] || (p.ID != defaultBotID && !botIDPattern.MatchString(p.ID)) ||
			p.AvatarSlot < 0 || p.AvatarSlot >= avatarSlots || slots[p.AvatarSlot] {
			return errors.New("Bot 列表含无效或重复的 ID 或头像")
		}
		ids[p.ID], slots[p.AvatarSlot] = true, true
	}
	return nil
}

func (bs *Bots) open(p BotProfile) (*Bot, error) {
	cfg := bs.main.cfg
	cfg.Name = p.Name
	// 实例级文件（env 等）在默认 Bot 的数据目录（REEVE_HOME）；必须在改 DataDir 之前取。
	cfg.RootDir = bs.main.cfg.RootDir
	if cfg.RootDir == "" {
		cfg.RootDir = bs.main.cfg.DataDir
	}
	cfg.DataDir = filepath.Join(cfg.DataDir, "bots", p.ID)
	cfg.Workspace = filepath.Join(bs.workRoot, p.ID)
	if bs.approval != nil {
		// 运行期新增的 Bot 也要拿到当前白名单，而不是启动时的快照。
		cfg.Rules.AllowShell = bs.approval.rules()
	}
	return newRuntime(cfg, bs.main, &p)
}

func (bs *Bots) path() string { return filepath.Join(bs.main.cfg.DataDir, "bots.json") }

func (bs *Bots) sharedMemoryPath() string {
	return filepath.Join(bs.main.cfg.DataDir, "shared-memory.md")
}

func (bs *Bots) readSharedMemory() (string, error) {
	bs.sharedMu.Lock()
	defer bs.sharedMu.Unlock()
	data, err := os.ReadFile(bs.sharedMemoryPath())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return strings.TrimSpace(string(data)), err
}

func (bs *Bots) writeSharedMemory(text, mode string) error {
	bs.sharedMu.Lock()
	defer bs.sharedMu.Unlock()
	return bs.main.writeMemoryFile(bs.sharedMemoryPath(), text, mode)
}

func (bs *Bots) save(profiles []BotProfile) error {
	tmp, err := os.CreateTemp(bs.main.cfg.DataDir, "bots-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	err = json.NewEncoder(tmp).Encode(profiles)
	closeErr := tmp.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), bs.path())
}

func (bs *Bots) Close() {
	bs.mu.Lock()
	if bs.closed {
		bs.mu.Unlock()
		return
	}
	bs.closed = true
	bs.mu.Unlock()
	if bs.push != nil {
		bs.push.close()
	}
	if bs.gateway != nil {
		bs.gateway.Close()
	}
	var wg sync.WaitGroup
	for _, b := range bs.runtimes {
		wg.Add(1)
		go func(b *Bot) { defer wg.Done(); b.Close() }(b)
	}
	wg.Wait()
	bs.retired.Wait()
}

// remove 删除 Bot。purge=false 时只从列表里移除，数据目录（聊天记录、记忆、密钥）和工作区都保留在磁盘上；
// purge=true 时在 Bot 停下之后把 data/bots/<id> 和它的工作区一起删掉，无法恢复。
func (bs *Bots) remove(id string, purge bool) error {
	if id == defaultBotID {
		return errors.New("默认 Bot 不能删除")
	}
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if bs.closed {
		return errors.New("服务正在关闭")
	}
	idx := slices.IndexFunc(bs.profiles, func(p BotProfile) bool { return p.ID == id })
	if idx < 0 {
		return fs.ErrNotExist
	}
	next := slices.Delete(slices.Clone(bs.profiles), idx, idx+1)
	if err := bs.save(next); err != nil {
		return err
	}
	b, endpoint := bs.runtimes[id], bs.handlers[id]
	bs.profiles = next
	delete(bs.runtimes, id)
	delete(bs.handlers, id)
	// No new requests can enter this endpoint. Stop its work and close its
	// stores after the requests that already entered have finished.
	bs.retired.Add(1)
	go func() {
		defer bs.retired.Done()
		b.stop()
		endpoint.requests.Wait()
		b.Close()
		if purge {
			bs.purge(b)
		}
	}()
	return nil
}

// purge 删除一个已关闭 Bot 的数据目录和工作区。路径都由系统生成（bot_ + 16 位十六进制），
// 这里再确认一次它们确实在 data/bots/ 和工作区根目录下面，防止误删。
func (bs *Bots) purge(b *Bot) {
	id := b.ID()
	if !botIDPattern.MatchString(id) {
		return
	}
	for _, p := range []struct{ dir, root string }{
		{b.cfg.DataDir, filepath.Join(bs.main.cfg.DataDir, "bots")},
		{b.cfg.Workspace, bs.workRoot},
	} {
		dir, err1 := filepath.Abs(p.dir)
		root, err2 := filepath.Abs(p.root)
		if err1 != nil || err2 != nil || filepath.Dir(dir) != root || filepath.Base(dir) != id {
			log.Printf("没有删除 %s：路径不在预期位置", p.dir)
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("删除 %s 失败：%v", dir, err)
		}
	}
}

// botDataPaths 返回 Bot 的数据目录和工作区（界面在删除确认里展示）。
func (bs *Bots) botDataPaths(id string) (data, workspace string) {
	return absPath(filepath.Join(bs.main.cfg.DataDir, "bots", id)), absPath(filepath.Join(bs.workRoot, id))
}

func (bs *Bots) setProfile(id string, p BotProfile) (BotProfile, error) {
	p.Name = strings.TrimSpace(p.Name)
	p.Instructions = strings.TrimSpace(p.Instructions)
	if p.Name == "" || utf8.RuneCountInString(p.Name) > 60 || utf8.RuneCountInString(p.Instructions) > 4000 {
		return BotProfile{}, errors.New("名称须为 1–60 字，职责说明最多 4000 字")
	}
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if bs.closed {
		return BotProfile{}, errors.New("服务正在关闭")
	}
	next := slices.Clone(bs.profiles)
	if id != "" {
		idx := slices.IndexFunc(next, func(x BotProfile) bool { return x.ID == id })
		if idx < 0 {
			return BotProfile{}, fs.ErrNotExist
		}
		p.ID, p.AvatarSlot = id, next[idx].AvatarSlot
		next[idx] = p
		if err := bs.save(next); err != nil {
			return BotProfile{}, err
		}
		bs.runtimes[id].profile.Store(&p)
	} else {
		if len(next) >= maxBots {
			return BotProfile{}, errors.New("Bot 数量已达上限")
		}
		p.ID = builtin.NewID("bot")
		used := [avatarSlots]bool{}
		for _, bot := range next {
			used[bot.AvatarSlot] = true
		}
		p.AvatarSlot = slices.Index(used[:], false)
		b, err := bs.open(p)
		if err != nil {
			return BotProfile{}, err
		}
		next = append(next, p)
		if err := bs.save(next); err != nil {
			b.Close()
			return BotProfile{}, err
		}
		bs.runtimes[p.ID], bs.handlers[p.ID] = b, &botEndpoint{Handler: b.Handler("")}
		b.coordinator = bs
		if bs.push != nil {
			bs.push.attach(b)
		}
		b.Start()
	}
	bs.profiles = next
	return p, nil
}

// Handler 是整个程序的 HTTP 入口：/api/bots/{id}/… 转给对应 Bot，/api/settings 等是全局设置，
// 其余路径提供前端页面（ui 为 nil 时只提供 API）。访问控制（密码登录）由调用方在外面包一层。
//
// 实例级环境变量 GET/PUT /api/env 在这里直接处理，所有 Bot 共用同一份。
// 命令定义是所有 Bot 共用的静态表，顶层 GET /api/commands 直接来自默认 Bot 的注册表；
// 执行命令是 bot 级行为（POST /api/bots/{id}/commands），顶层不提供。
func (bs *Bots) Handler(ui fs.FS) http.Handler {
	mux := http.NewServeMux()
	mainAPI := bs.main.Handler("")
	if ui != nil {
		mux.Handle("/", spa(ui))
	}
	// 全局接口（设置、更新检查）挂在默认 Bot 上；其他 /api/… 只能通过 /api/bots/{id}/… 访问。
	for _, p := range []string{"/api/settings", "/api/settings/", "/api/channels/", "POST /api/channels", "GET /api/update"} {
		mux.Handle(p, mainAPI)
	}
	mux.HandleFunc("GET /api/channels", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, bs.channelsView(), nil)
	})
	// 实例级环境变量：所有 Bot 共用同一份（<实例目录>/env）。
	mux.HandleFunc("GET /api/env", func(w http.ResponseWriter, r *http.Request) {
		vars, err := bs.main.InstanceEnv()
		if vars == nil {
			vars = map[string]string{}
		}
		writeJSON(w, envVars{Vars: vars}, err)
	})
	mux.HandleFunc("PUT /api/env", func(w http.ResponseWriter, r *http.Request) {
		handleEnvPut(w, r, bs.main.ReplaceInstanceEnv)
	})
	// 实例级 ntfy 推送：地址/topic/认证凭据和三个事件开关，配置存 <实例目录>/push.json。
	mux.HandleFunc("GET /api/push", func(w http.ResponseWriter, r *http.Request) {
		if bs.push == nil {
			writeJSON(w, pushView{}, nil)
			return
		}
		writeJSON(w, bs.push.view(), nil)
	})
	mux.HandleFunc("PUT /api/push", func(w http.ResponseWriter, r *http.Request) {
		if bs.push == nil {
			writeJSON(w, pushView{}, errors.New("推送未初始化"))
			return
		}
		var d pushDraft
		if !decodeJSON(w, r, &d, 16<<10) {
			return
		}
		view, err := bs.push.update(d)
		writeJSON(w, view, err)
	})
	mux.HandleFunc("POST /api/push/test", func(w http.ResponseWriter, r *http.Request) {
		if bs.push == nil {
			writeJSON(w, map[string]bool{"ok": false}, errors.New("推送未初始化"))
			return
		}
		err := bs.push.test(r.Context())
		writeJSON(w, map[string]bool{"ok": err == nil}, err)
	})
	// 实例级自动审批命令白名单：所有 Bot 共用；命中后 shell 命令不再弹审批卡。
	mux.HandleFunc("GET /api/approvals/whitelist", func(w http.ResponseWriter, r *http.Request) {
		if bs.approval == nil {
			writeJSON(w, approvalWhitelistView{Commands: []string{}}, nil)
			return
		}
		writeJSON(w, bs.approval.view(), nil)
	})
	mux.HandleFunc("PUT /api/approvals/whitelist", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Commands []string `json:"commands"`
		}
		if !decodeJSON(w, r, &body, 64<<10) {
			return
		}
		view, err := bs.saveApprovalWhitelist(body.Commands)
		writeJSON(w, view, err)
	})
	mux.HandleFunc("GET /api/commands", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, commandList{Commands: bs.main.commandInfos()}, nil)
	})
	mux.HandleFunc("/api/", http.NotFound)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "version": currentVersion(), "api_version": 1}, nil)
	})
	mux.HandleFunc("GET /api/capabilities", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"api_version": 1, "version": currentVersion(),
			"clients": []string{"web", "android"},
			"auth": map[string]any{
				"login": "/api/auth/login", "session": "cookie|bearer",
				"status_path": "/api/auth/me", "password_change": "/api/auth/password",
			},
			"events": map[string]any{"transport": "sse", "resume": "Last-Event-ID", "heartbeat_sec": 20},
			"limits": map[string]any{"json_bytes": maxJSONBody, "upload_file_bytes": maxUpload,
				"upload_request_bytes": maxUpload + (1 << 20), "upload_files": 16, "bots": maxBots},
		}, nil)
	})
	mux.HandleFunc("GET /api/bots", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, bs.localProfiles(), nil)
	})
	save := func(w http.ResponseWriter, r *http.Request) {
		var p BotProfile
		if !decodeJSON(w, r, &p, 64<<10) {
			return
		}
		p, err := bs.setProfile(r.PathValue("id"), p)
		if errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, p, err)
	}
	mux.HandleFunc("POST /api/bots", save)
	mux.HandleFunc("PATCH /api/bots/{id}", save)
	mux.HandleFunc("GET /api/bots/{id}/paths", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		bs.mu.RLock()
		_, ok := bs.runtimes[id]
		bs.mu.RUnlock()
		if !ok || id == defaultBotID {
			http.NotFound(w, r)
			return
		}
		data, ws := bs.botDataPaths(id)
		writeJSON(w, map[string]string{"data_dir": data, "workspace": ws}, nil)
	})
	mux.HandleFunc("DELETE /api/bots/{id}", func(w http.ResponseWriter, r *http.Request) {
		err := bs.remove(r.PathValue("id"), r.URL.Query().Get("purge") == "1")
		if errors.Is(err, fs.ErrNotExist) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]bool{"ok": true}, err)
	})
	mux.HandleFunc("POST /api/bots/{id}/settings/copy", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			From string `json:"from"`
		}
		if !decodeJSON(w, r, &body, 1024) {
			return
		}
		saved, err := bs.copySettings(body.From, r.PathValue("id"))
		if errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, saved, err)
	})
	mux.HandleFunc("/api/bots/{id}/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		rest := r.PathValue("rest")
		if rest == "update" {
			http.NotFound(w, r)
			return
		}
		bs.serveBotRoute(w, r, r.PathValue("id"), rest)
	})
	return mux
}

func (bs *Bots) serveBotRoute(w http.ResponseWriter, r *http.Request, id, rest string) {
	_, endpoint, release := bs.acquireBotEndpoint(id)
	defer release()
	if endpoint == nil {
		http.NotFound(w, r)
		return
	}
	scoped := r.Clone(r.Context())
	scoped.URL.Path, scoped.URL.RawPath = "/api/"+rest, ""
	endpoint.ServeHTTP(w, scoped)
}

// copySettings 把一个 Bot 的模型选择（聊天模型、Agent 模型与执行方式）复制给另一个 Bot。
// API 渠道是整台设备共用的，不需要复制。复制之后两边各自独立。
func (bs *Bots) copySettings(fromID, toID string) (APISettings, error) {
	if fromID == toID {
		return APISettings{}, errors.New("请选择另一个 Bot 作为来源")
	}
	src, releaseSrc := bs.acquireBot(fromID)
	defer releaseSrc()
	dst, releaseDst := bs.acquireBot(toID)
	defer releaseDst()
	if src == nil || dst == nil {
		return APISettings{}, fs.ErrNotExist
	}
	src.modelMu.RLock()
	draft := APISettings{DefaultProviderID: src.defaultChannel, Model: src.chatModel,
		AgentProviderID: src.agentChannel, AgentModel: src.agentModelName}
	src.modelMu.RUnlock()
	executors := src.Executors()
	if !validRoute(dst.pool.public(), draft.DefaultProviderID, draft.Model) {
		draft.DefaultProviderID, draft.Model = "", ""
	}
	if !validRoute(dst.pool.public(), draft.AgentProviderID, draft.AgentModel) {
		draft.AgentProviderID, draft.AgentModel = draft.DefaultProviderID, draft.Model
	}
	if _, err := dst.saveRoutes(draft); err != nil {
		return APISettings{}, fmt.Errorf("复制模型选择失败: %w", err)
	}
	if err := dst.SaveExecutors(executors); err != nil {
		return APISettings{}, fmt.Errorf("模型已复制，但执行器设置没有复制成功: %w", err)
	}
	return dst.GetSettings(), nil
}

// saveApprovalWhitelist 保存实例级自动审批白名单，并热更新所有已运行 Bot 的 Guard。
// 先落盘/更新管理器状态，再拿 bs.mu 应用；和 setProfile 新建 Bot 的路径串行，避免新 Bot 拿到旧规则。
func (bs *Bots) saveApprovalWhitelist(commands []string) (approvalWhitelistView, error) {
	if bs.approval == nil {
		return approvalWhitelistView{}, errors.New("自动审批白名单未初始化")
	}
	bs.approvalMu.Lock()
	defer bs.approvalMu.Unlock()
	rules, err := bs.approval.save(commands)
	if err != nil {
		return approvalWhitelistView{}, err
	}
	bs.mu.Lock()
	defer bs.mu.Unlock()
	for _, b := range bs.runtimes {
		if b == nil {
			continue
		}
		if err := b.guard.SetAllowShell(rules); err != nil {
			return approvalWhitelistView{}, err
		}
	}
	return bs.approval.view(), nil
}

// channelsView 列出设备上的 API 渠道，并附上每个 Bot 的聊天 / Agent 各用了哪个渠道。
func (bs *Bots) channelsView() channelsView {
	bs.mu.RLock()
	profiles := slices.Clone(bs.profiles)
	bots := make([]*Bot, len(profiles))
	for i, p := range profiles {
		bots[i] = bs.runtimes[p.ID]
	}
	bs.mu.RUnlock()
	view := channelsView{Providers: bs.main.pool.public(), Usage: []channelUse{}}
	for i, b := range bots {
		if b == nil {
			continue
		}
		b.modelMu.RLock()
		agentCLI := b.executors.Default
		use := channelUse{BotID: profiles[i].ID, Name: profiles[i].Name, ChatProviderID: b.defaultChannel, AgentCLI: agentCLI}
		if agentCLI == string(exec.KindBuiltin) {
			use.AgentCLI = ""
			use.AgentProviderID = b.agentChannel
		}
		b.modelMu.RUnlock()
		view.Usage = append(view.Usage, use)
	}
	return view
}
