package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kiry163/reeve/internal/exec/cli"
)

// Handler 返回 HTTP 接口（以及可选的前端静态文件）。
//
//	GET  /api/info                 Bot 名字、模型等
//	GET  /api/stream               SSE：所有事件（支持 Last-Event-ID 断线续传）
//	POST /api/messages             {"text":"..."}
//	POST /api/stop                 停止所有任务（确定性，不经过模型）
//	POST /api/approvals/{id}       {"decision":"once|session|deny"}  —— 只能由用户点按钮触发
//	POST /api/cards/{id}           {"answer":"..."} 或 {"dismiss":true}
//	POST /api/regenerate           重新生成最后一条回复
//	POST /api/messages/{id}/edit   编辑并重发最后一条用户消息
//	POST /api/messages/{id}/react  {"emoji":"👍"}（再点一次取消）
//	POST /api/uploads              multipart 上传附件（存进工作区 uploads/）
//	GET  /api/files?path=…         打开/下载工作区里的文件（&download=1 下载）
//	POST /api/tasks/{id}/{action}  pause | resume | cancel | retry | top | up
//	GET  /api/secrets              密钥列表（只有名字、说明、时间，永远没有值）
//	POST /api/secrets              {"request_id":"sec_…","value":"…"}（或 {"name","value","description"}）
//	                               值只经过这一个接口，直接加密落盘，不进聊天记录/事件/日志
//	GET  /api/schedules             定时计划列表
//	GET  /api/schedules/{id}         单个计划
//	POST /api/schedules/{id}/{action} pause|resume|run|delete（界面按钮；创建/大改仍走聊天层审批）
//	POST /api/secrets/requests/{id}/cancel   取消密钥卡片（= 拒绝提供）
//	GET/PUT /api/env               本 Bot 的 bot 级环境变量（整体替换）
//	POST /api/commands             {"line":"/env list"} 执行一行斜线命令，返回 {"text","ok"}
//	                               非命令（不以 / 开头）返回 400；command.result 事件由 CommandLine 写
//	GET  /api/skills               技能索引（含加载错误；path 是 skills/<目录>/SKILL.md 相对标签）
//	GET  /api/executors            本 Bot 的执行器白名单、默认执行器与 CLI 选项
//	PUT  /api/executors            整体替换（用户操作，直接生效；模型没有修改白名单的工具）
//
// 实例级环境变量（所有 Bot 共用）在 Bots.Handler 的顶层 /api/env，语义相同；
// 命令定义是全局静态表，在顶层 GET /api/commands。
func (b *Bot) Handler(webDir string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, b.GetSettings(), nil)
	})
	mux.HandleFunc("POST /api/settings", func(w http.ResponseWriter, r *http.Request) {
		var payload APISettings
		if !decodeJSON(w, r, &payload, maxJSONBody) {
			return
		}
		saved, err := b.SaveSettings(payload)
		writeJSON(w, saved, err)
	})
	// 只保存本 Bot 用哪个渠道的哪个模型（渠道本身在 /api/channels 管理，整台设备共用）。
	mux.HandleFunc("POST /api/settings/routes", func(w http.ResponseWriter, r *http.Request) {
		var payload APISettings
		if !decodeJSON(w, r, &payload, maxJSONBody) {
			return
		}
		saved, err := b.saveRoutes(payload)
		writeJSON(w, saved, err)
	})
	mux.HandleFunc("GET /api/channels", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, channelsView{Providers: b.pool.public(), Usage: []channelUse{}}, nil)
	})
	mux.HandleFunc("POST /api/channels", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Providers []APIChannel `json:"providers"`
		}
		if !decodeJSON(w, r, &payload, maxJSONBody) {
			return
		}
		if payload.Providers == nil {
			writeJSON(w, nil, errors.New("缺少渠道列表"))
			return
		}
		saved, err := b.pool.save(payload.Providers)
		writeJSON(w, channelsView{Providers: saved, Usage: []channelUse{}}, err)
	})
	for _, prefix := range []string{"/api/settings", "/api/channels"} {
		mux.HandleFunc("POST "+prefix+"/models", func(w http.ResponseWriter, r *http.Request) {
			var p APISettings
			if !decodeJSON(w, r, &p, maxJSONBody) {
				return
			}
			models, err := b.FetchRemoteModels(r.Context(), p)
			writeJSON(w, map[string]any{"models": models}, err)
		})
		mux.HandleFunc("POST "+prefix+"/test", func(w http.ResponseWriter, r *http.Request) {
			var p APISettings
			if !decodeJSON(w, r, &p, maxJSONBody) {
				return
			}
			latency, err := b.TestConnection(r.Context(), p)
			writeJSON(w, map[string]any{"latency_ms": latency}, err)
		})
	}
	mux.HandleFunc("GET /api/settings/connections", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		// probe=0：只刷新安装状态与已缓存的版本，不起子进程（安装轮询用）。
		writeJSON(w, b.connections(r.Context(), r.URL.Query().Get("probe") != "0"), nil)
	})
	// 一键安装 / 更新 CLI（整台设备共用，装到 <data>/tools）。进度通过 GET /api/settings/connections 的 clis[].install 查看。
	mux.HandleFunc("POST /api/settings/clis/install", func(w http.ResponseWriter, r *http.Request) {
		var draft struct {
			ID string `json:"id"`
		}
		if !decodeJSON(w, r, &draft, 256) {
			return
		}
		if err := cli.StartInstall(draft.ID); err != nil {
			writeJSON(w, nil, err)
			return
		}
		writeJSON(w, b.connections(r.Context(), true), nil)
	})
	mux.HandleFunc("GET /api/update", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		status, err := checkUpdate(r.Context(), releaseClient, latestReleaseAPI, currentVersion())
		writeJSON(w, status, err)
	})
	mux.HandleFunc("GET /api/info", func(w http.ResponseWriter, r *http.Request) {
		_, chatModel := b.resolveModel(true)
		_, agentModel := b.resolveModel(false)
		conn := b.connectionSettings()
		writeJSON(w, map[string]any{"name": b.botProfile().Name, "chat_model": chatModel, "agent_model": agentModel,
			"agent_cli": conn.CLI, "agent_cli_model": conn.Model, "agent_cli_auth": conn.Auth,
			"policy": b.cfg.Policy, "demo": b.cfg.Demo, "max_concurrent": b.cfg.MaxConcurrent,
			"stall_sec": int(b.cfg.StallTimeout.Seconds()), "timezone": "Asia/Shanghai", "version": currentVersion()}, nil)
	})
	mux.HandleFunc("POST /api/messages", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text  string
			Files []Attachment
		}
		if !decodeJSON(w, r, &body, maxJSONBody) {
			return
		}
		m, err := b.Send(body.Text, body.Files...)
		writeJSON(w, m, err)
	})
	mux.HandleFunc("POST /api/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("scope") == "reply" { // Esc：只停聊天层正在写的回复
			writeJSON(w, map[string]bool{"stopped": b.StopReply()}, nil)
			return
		}
		writeJSON(w, map[string]int{"stopped": b.Stop()}, nil)
	})
	mux.HandleFunc("POST /api/regenerate", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"ok": true}, b.Regenerate())
	})
	mux.HandleFunc("POST /api/messages/{id}/edit", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Text string }
		if !decodeJSON(w, r, &body, maxJSONBody) {
			return
		}
		m, err := b.EditLast(r.PathValue("id"), body.Text)
		writeJSON(w, m, err)
	})
	mux.HandleFunc("POST /api/messages/{id}/react", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Emoji string }
		if !decodeJSON(w, r, &body, 1024) {
			return
		}
		writeJSON(w, map[string]bool{"ok": true}, b.React(r.PathValue("id"), body.Emoji))
	})
	mux.HandleFunc("POST /api/uploads", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxUpload+(1<<20))
		mr, err := r.MultipartReader()
		if err != nil {
			writeJSON(w, nil, fmt.Errorf("需要 multipart 上传"))
			return
		}
		out := []Attachment{}
		committed := false
		defer func() {
			if !committed {
				for _, a := range out {
					_ = os.Remove(filepath.Join(b.cfg.Workspace, filepath.FromSlash(a.Path)))
				}
			}
		}()
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				writeJSON(w, nil, fmt.Errorf("上传未完成或超过大小限制"))
				return
			}
			if part.FileName() == "" {
				_ = part.Close()
				continue
			}
			if len(out) >= 16 {
				writeJSON(w, nil, fmt.Errorf("一次最多上传 16 个文件"))
				return
			}
			a, err := b.Upload(part.FileName(), part)
			if err != nil {
				writeJSON(w, nil, err)
				return
			}
			out = append(out, a)
		}
		if len(out) == 0 {
			writeJSON(w, nil, fmt.Errorf("请选择要上传的文件"))
			return
		}
		committed = true
		writeJSON(w, out, nil)
	})
	mux.HandleFunc("GET /api/files", b.serveFile)
	mux.HandleFunc("POST /api/tasks/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		msg, err := b.TaskAction(r.PathValue("id"), r.PathValue("action"))
		writeJSON(w, map[string]string{"message": msg}, err)
	})
	mux.HandleFunc("POST /api/approvals/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Decision string
		}
		if !decodeJSON(w, r, &body, 4096) {
			return
		}
		err := b.Decide(r.PathValue("id"), body.Decision)
		writeJSON(w, map[string]bool{"ok": true}, err)
	})
	mux.HandleFunc("POST /api/cards/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Answer  string
			Dismiss bool
		}
		if !decodeJSON(w, r, &body, maxJSONBody) {
			return
		}
		writeJSON(w, map[string]bool{"ok": true}, b.AnswerCard(r.PathValue("id"), body.Answer, body.Dismiss))
	})
	mux.HandleFunc("GET /api/secrets", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, b.vault.List(), nil)
	})
	// 本 Bot 的 bot 级环境变量。实例级（所有 Bot 共用）是顶层 /api/env，
	// 由 Bots.Handler 直接处理；/api/bots/{id}/env 经过前缀转发后落到这里。
	mux.HandleFunc("GET /api/env", func(w http.ResponseWriter, r *http.Request) {
		vars, err := b.BotEnv()
		if vars == nil {
			vars = map[string]string{}
		}
		writeJSON(w, envVars{Vars: vars}, err)
	})
	mux.HandleFunc("PUT /api/env", func(w http.ResponseWriter, r *http.Request) {
		handleEnvPut(w, r, b.ReplaceBotEnv)
	})
	// 执行一行斜线命令。结果由 CommandLine 写成 command.result 事件，这里只回 HTTP 响应。
	mux.HandleFunc("POST /api/commands", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Line string `json:"line"`
		}
		if !decodeJSON(w, r, &body, maxJSONBody) {
			return
		}
		line := strings.TrimSpace(body.Line)
		if !strings.HasPrefix(line, "/") {
			writeJSON(w, nil, errors.New("line 必须以 / 开头，例如 /env list"))
			return
		}
		if len(line) > maxCommandLine {
			writeJSON(w, nil, fmt.Errorf("line 太长（%d 字节）：一行命令最多 %d 字节", len(line), maxCommandLine))
			return
		}
		res, _ := b.CommandLine(r.Context(), line)
		writeJSON(w, commandResult{Text: res.Text, OK: res.OK}, nil)
	})
	// 技能索引：只读，与启动时加载的集合一致（没有 reload 端点）。
	mux.HandleFunc("GET /api/skills", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, b.SkillIndex(), nil)
	})
	// 执行器白名单与默认执行器。PUT 是用户操作，直接生效；校验失败时旧值不变。
	mux.HandleFunc("GET /api/executors", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, b.Executors(), nil)
	})
	mux.HandleFunc("PUT /api/executors", func(w http.ResponseWriter, r *http.Request) {
		var in Executors
		if !decodeJSON(w, r, &in, maxJSONBody) {
			return
		}
		err := b.SaveExecutors(in)
		if err != nil {
			// 校验/写盘错误可能内嵌数据目录的绝对路径；HTTP 响应里只给相对标签。
			err = errors.New(b.redactDataDir(err.Error()))
		}
		writeJSON(w, b.Executors(), err)
	})
	mux.HandleFunc("POST /api/secrets", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RequestID   string `json:"request_id"`
			Name        string `json:"name"`
			Value       string `json:"value"`
			Description string `json:"description"`
		}
		if !decodeJSON(w, r, &body, 64<<10) {
			return
		}
		name, err := b.SaveSecret(body.RequestID, body.Name, body.Value, body.Description)
		body.Value = ""
		writeJSON(w, map[string]any{"ok": true, "name": name}, err)
	})
	mux.HandleFunc("POST /api/secrets/requests/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"ok": true}, b.CancelSecret(r.PathValue("id")))
	})
	mux.HandleFunc("GET /api/schedules", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, b.Schedules(), nil)
	})
	mux.HandleFunc("GET /api/schedules/{id}", func(w http.ResponseWriter, r *http.Request) {
		p, err := b.findSchedule(r.PathValue("id"))
		if err != nil {
			writeJSON(w, nil, err)
			return
		}
		writeJSON(w, p.Public(), nil)
	})
	mux.HandleFunc("POST /api/schedules/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		id, action := r.PathValue("id"), r.PathValue("action")
		p, err := b.findSchedule(id)
		if err != nil {
			writeJSON(w, nil, err)
			return
		}
		switch action {
		case "pause":
			p2, err := b.sched.SetEnabled(p.ID, false)
			writeJSON(w, p2.Public(), err)
		case "resume":
			p2, err := b.sched.SetEnabled(p.ID, true)
			writeJSON(w, p2.Public(), err)
		case "run":
			c, err := b.sched.RunNow(p.ID)
			writeJSON(w, map[string]any{"claim": c, "task_id": c.TaskID}, err)
		case "delete":
			writeJSON(w, map[string]bool{"ok": true}, b.sched.Remove(p.ID))
		default:
			writeJSON(w, nil, fmt.Errorf("未知操作 %s（pause|resume|run|delete）", action))
		}
	})
	mux.HandleFunc("GET /api/stream", b.serveStream)
	if webDir != "" {
		mux.Handle("/", spa(os.DirFS(webDir)))
	}
	return mux
}

func (b *Bot) serveStream(w http.ResponseWriter, r *http.Request) {
	b.streamEvents(w, r)
}

func (b *Bot) streamEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	last, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	if v := r.URL.Query().Get("after"); v != "" && last == 0 {
		last, _ = strconv.ParseInt(v, 10, 64)
	}
	b.turnMu.Lock()
	live, unsub := b.store.Subscribe() // 先订阅再补发历史，避免漏事件
	busy := b.turnCancel != nil
	b.turnMu.Unlock()
	defer unsub()
	send := func(e Event) {
		if r.Context().Err() != nil {
			return
		}
		if e.Seq > 0 {
			if e.Seq <= last {
				return
			}
			last = e.Seq
			fmt.Fprintf(w, "id: %d\n", e.Seq)
		}
		data, _ := json.Marshal(e)
		fmt.Fprintf(w, "data: %s\n\n", b.store.Scrub(data)) // 历史事件可能早于某个密钥的保存，推送前再脱敏一次
	}
	for _, e := range b.store.Since(last) {
		send(e)
	}
	busyData, _ := json.Marshal(map[string]bool{"busy": busy})
	send(Event{Kind: "replay.done", Time: time.Now(), Data: busyData})
	flusher.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case e, ok := <-live:
			if !ok {
				return // 太慢被断开，浏览器会自动重连补齐
			}
			if !e.Internal {
				send(e)
				flusher.Flush()
			}
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		case <-b.ctx.Done():
			return
		}
	}
}

// spa 提供前端文件；找不到的路径都回退到 index.html（前端路由）。
func spa(web fs.FS) http.Handler {
	files := http.FileServer(http.FS(web))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(web, "index.html"); err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprintln(w, "前端还没构建：cd web && npm install && npm run build（开发时用 npm run dev）")
			return
		}
		// fs.FS 在所有平台（包括 Windows）上都用 / 分隔路径。
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if st, err := fs.Stat(web, name); name == "" || err != nil || st.IsDir() {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFileFS(w, r, web, "index.html")
			return
		}
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

func absPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// envVars 是 GET /api/env 的响应。
type envVars struct {
	Vars map[string]string `json:"vars"`
}

// envVarsBody 是 PUT /api/env 的请求体。用指针区分「没传 vars」和「传了空对象」：
// 前者是客户端写错了，后者是明确的「清空全部变量」。
type envVarsBody struct {
	Vars *map[string]string `json:"vars"`
}

func (b envVarsBody) vars() (map[string]string, error) {
	if b.Vars == nil {
		return nil, errors.New(`缺少 vars；要清空全部变量请显式传 {"vars":{}}`)
	}
	return *b.Vars, nil
}

// envReplaceResult 是 PUT /api/env 的响应；Warnings 初始化为空切片，序列化成 [] 而不是 null。
type envReplaceResult struct {
	OK       bool     `json:"ok"`
	Warnings []string `json:"warnings"`
}

// handleEnvPut 统一实例级与 bot 级 PUT 的处理：decode → 取 vars → 整体替换 → 响应，
// 让两个端点共用同一套错误文案与响应形状。
func handleEnvPut(w http.ResponseWriter, r *http.Request, replace func(map[string]string) ([]string, error)) {
	var body envVarsBody
	if !decodeJSON(w, r, &body, maxJSONBody) {
		return
	}
	vars, err := body.vars()
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	warnings, err := replace(vars)
	writeJSON(w, envReplaceResult{OK: err == nil, Warnings: warnings}, err)
}

// commandInfo 是命令定义在 HTTP 层的样子；执行函数不进响应。
type commandInfo struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
	Summary string   `json:"summary"`
	Usage   string   `json:"usage"`
}

// commandList 是 GET /api/commands 的响应；Commands 永远不是 null。
type commandList struct {
	Commands []commandInfo `json:"commands"`
}

// commandResult 是 POST /api/commands 的响应；command.result 事件由 CommandLine 写，这里不重复写。
type commandResult struct {
	Text string `json:"text"`
	OK   bool   `json:"ok"`
}

// commandInfos 按注册顺序转出命令定义；aliases 为空时序列化成 [] 而不是 null。
func (b *Bot) commandInfos() []commandInfo {
	out := []commandInfo{}
	if b.commands == nil {
		return out
	}
	for _, d := range b.commands.Definitions() {
		aliases := d.Aliases
		if aliases == nil {
			aliases = []string{}
		}
		out = append(out, commandInfo{Name: d.Name, Aliases: aliases, Summary: d.Summary, Usage: d.Usage})
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		code := 400
		if errors.Is(err, ErrTaskEnded) {
			code = http.StatusConflict
		}
		w.WriteHeader(code)
		v = map[string]string{"error": err.Error()}
	}
	_ = json.NewEncoder(w).Encode(v)
}
