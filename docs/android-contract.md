# reeve Android 客户端契约

本文件是 Android（及未来其它原生客户端）的**唯一契约来源**。内容从 Go 实现抽取（分支 `main`，M3 之后），字段名以 struct tag 为准；与设计文档冲突时以本文件+代码为准。

所有路径都在 `/api` 下。Bot 作用域用 `B = /api/bots/{id}` 表示（`id` 为 `main` 或 `bot_<16 hex>`）。

---

## 1. 鉴权

| 项 | 值 |
|---|---|
| token 文件 | `<DataDir>/token`（`DataDir` = `$REEVE_HOME`，否则 `~/.reeve`），`0600`，32 字节 base64url |
| 配对串 | `reeve://pair?v=1&host=<addr>&token=<token>` |
| Bearer | `Authorization: Bearer <token>`（前缀必须字面 `"Bearer "`） |
| Cookie | `reeve_session`（网页用；Android 不需要） |
| 回环免认证 | 仅当 Host 与 RemoteAddr 同时是本机；**局域网 IP 与模拟器的 `10.0.2.2` 都不算** |
| 轮换 | `reeve token rotate` 后旧 token 立即失效 → 401 |

`/api/auth/me` → `{"ok":true,"enabled":true,"authenticated":bool,"mode":"loopback|token|session|none"}`。
未认证访问 `/api/*` → `401 {"error":"需要登录：…"}`。Android 请求**不要**带 `Origin` 头。

---

## 2. 端点

通用：业务错误 `{"error":"中文说明"}`，除 `409` 外都是 `400`；未知路径 404 是**纯文本**。写请求必须 `Content-Type: application/json` 且 body 是一个完整 JSON 对象。

### 2.1 顶层

| method path | 请求 | 响应 |
|---|---|---|
| `GET /api/health` | — | `{"ok":true,"version":"…","api_version":1}` |
| `GET /api/capabilities` | — | `{"api_version":1,"version":…,"clients":["web","android"],"auth":{…},"events":{"transport":"sse","resume":"Last-Event-ID","heartbeat_sec":20},"limits":{"json_bytes":1048576,"upload_file_bytes":26214400,"upload_request_bytes":27262976,"upload_files":16,"bots":32}}` |
| `GET /api/bots` | — | `[BotProfile{id,name,instructions,avatarSlot}]` |
| `POST /api/bots` | `{name,instructions}` | `BotProfile` |
| `PATCH /api/bots/{id}` | `{name,instructions}` | `BotProfile` |
| `DELETE /api/bots/{id}?purge=1` | — | `{"ok":true}`（`main` → 400） |
| `GET /api/env` `PUT /api/env` | PUT `{"vars":{K:V}}`（**整体替换**） | `{"vars":{K:V}}` / `{"ok":bool,"warnings":[…]}` |
| `GET /api/commands` | — | `{"commands":[{name,aliases:[],summary,usage}]}` |
| `GET /api/channels` `POST /api/channels` | `{"providers":[APIChannel]}` | `{"providers":[APIChannel],"usage":[{bot_id,name,chat_provider_id,agent_provider_id,agent_cli}]}` |
| `GET /api/settings` `POST /api/settings` | `APISettings` | `APISettings` |
| `POST /api/settings/routes` | `{default_provider_id,model,agent_provider_id,agent_model}` | `APISettings` |
| `POST /api/settings/models` `POST /api/settings/test` | `APISettings` | `{"models":[…]} ` / `{"latency_ms":n}` |
| `GET /api/settings/connections?probe=0` | — | `{cli,cli_model,cli_auth,cli_channel,clis:[{id,name,installed,subscription,login,model_option,models,path,version,probed,available,note}]}` |
| `GET /api/update` | — | `{current,latest,release_url,state}` |

`APIChannel`：`id,name,base_url,enabled,auth_required,api_key?`（只写；省略=沿用旧值）、`has_api_key`、`models[]`、`hidden_models[]`。
`APISettings`：`base_url,model,api_key?,has_api_key,providers?,default_provider_id?,agent_provider_id?,agent_model?,provider_id?,env_override?,env_model?,env_agent_model?`。

### 2.2 Bot 级 — 信息/聊天/附件

| method path | 请求 | 响应 |
|---|---|---|
| `GET B/info` | — | `{name,chat_model,agent_model,agent_cli,agent_cli_model,agent_cli_auth,policy,demo,max_concurrent,stall_sec,timezone,version}` |
| `POST B/messages` | `{"text":"…","files":[Attachment]}` | `Message`（`/` 开头时返回合成 `{"role":"notice","text":…}`，结果在 `command.result`） |
| `POST B/stop` | — | `{"stopped":n}`；`?scope=reply` → `{"stopped":bool}` |
| `POST B/regenerate` | — | `{"ok":true}` |
| `POST B/messages/{id}/edit` | `{"text":"…"}` | `Message`（仅最后一条用户消息） |
| `POST B/messages/{id}/react` | `{"emoji":"👍"}` | `{"ok":true}` |
| `POST B/uploads` | multipart（≤16 文件，单文件 ≤25 MiB） | `[{path,name,size,mime}]` |
| `GET B/files?path=…&download=1` | — | 字节流（错误是纯文本） |

`Message`：`id,role(user|assistant|notice),text,card?,tools?,secret?,files?,reactions?,hidden?,silent?,thinking?,thinking_ms?,created_at`。
`Attachment`：`path,name,size,mime?`。`Reaction`：`by(user|bot),emoji`。

附件流程：先 `POST B/uploads` 拿 `path`，再放进 `POST B/messages.files`。

### 2.3 Bot 级 — 任务/审批/卡片/密钥

| method path | 请求 | 响应 |
|---|---|---|
| `POST B/tasks/{id}/{action}` | — | `{"message":"…"}`；action ∈ `pause｜resume｜cancel｜retry｜top｜up`；`retry` 的 message 是新任务 ID |
| `POST B/approvals/{id}` | `{"decision":"once"|"session"|"deny"}` | `{"ok":true}`；任务已终态 → `409` |
| `POST B/cards/{id}` | `{"answer":"…"}` 或 `{"dismiss":true}` | `{"ok":true}` |
| `GET B/secrets` | — | `[{name,description,scope:{tools[],domains[]},created_at,updated_at,last_used}]`（永不含值） |
| `POST B/secrets` | `{request_id?,name,value,description?}` | `{"ok":true,"name":"…"}` |
| `POST B/secrets/requests/{id}/cancel` | — | `{"ok":true}` |

### 2.4 Bot 级 — 定时计划

`GET B/schedules`、`GET B/schedules/{id}`、`POST B/schedules/{id}/{pause|resume|run|delete}`。
`Schedule`：`id,title,prompt,enabled,overlap(skip|queue),kind(once|daily|weekly),timezone,hour,minute,weekdays?,at_local?,next_run?,next_run_local?,last_run?,last_run_local?,last_status?,last_task_id?,last_claim?,created_at,updated_at`。

### 2.5 Bot 级 — 设置/执行器/命令/技能/流

| method path | 请求 | 响应 |
|---|---|---|
| `GET B/settings` `POST B/settings` `POST B/settings/routes` `POST B/settings/models` `POST B/settings/test` | 同顶层 | 同顶层 |
| `GET B/settings/connections?probe=0` | — | 同 `GET /api/settings/connections`：`{cli,cli_model,cli_auth,cli_channel,clis:[{id,name,installed,subscription,login,model_option,models,path,version,probed,available,note}]}` |
| `GET B/executors` `PUT B/executors` | `{"allowed":["builtin","omp"],"default":"builtin","clis":{"omp":{"cli_model":"…","cli_auth":"subscription|env|channel","cli_channel":"…"}}}` | 同形状；校验失败 400 且旧值不变 |
| `POST B/commands` | `{"line":"/env list"}` | `{"text":"…","ok":true}`；非 `/` 开头或 >4096 字节 → 400 |
| `GET B/skills` | — | `{"skills":[{name,description,when_to_use,path}],"errors":[…]} ` |
| `GET B/stream` | — | SSE（见 §3） |

**没有** `GET /api/tasks`、`/api/approvals`、`/api/cards` 列表端点——状态必须靠 `/stream` 重放重建。

---

## 3. SSE

`GET B/stream`，续传：`Last-Event-ID: <seq>`（优先）或 `?after=<seq>`。

帧格式：

```
id: 42
data: {"seq":42,"kind":"message","time":"…","data":{…}}

: ping
```

- `id:` 只有持久事件（`seq>0`）才有；`seq=0` 的临时事件**不能**更新续传位点。
- 顺序：先补发 `seq>last`，再 `replay.done`，然后实时流。
- 订阅缓冲 2048；消费过慢会被服务端断开 → 带 `Last-Event-ID` 重连补齐。
- 心跳 20 秒一次（注释行）；无 `retry:` 字段。

持久事件（`seq>0`）：

| kind | data |
|---|---|
| `message` | `Message`（`role=notice` 永不推送） |
| `message.hidden` | `["msg_…"]` |
| `message.reaction` | `{id,by,emoji,on}` |
| `card` | `{id,question,options[],status,answer?}` |
| `secret.card` | `{id,name,description?,status,exists?}` |
| `command.result` | `{command,text,ok}` |
| `task.created` | `TaskRec` |
| `task.updated` | `{id,status?,prio?,error?,stop_reason?,clear_stop_reason?}` |
| `task.pausing` | `{id,stop_reason}` |
| `task.prio` | `{id,prio}` |
| `task.note` | `{id,text}` |
| `task.summary` | `{id,status,summary,steps,files?,duration_sec,error?}` |
| `env.set`/`env.unset` | `{layer(instance|bot),name,source(user|builtin)}`（永不含值） |
| `executors.updated` | `{allowed[],default}` |
| `executors.dropped` | `{kinds[]}` |
| `schedule.upserted` | `Schedule` |
| `schedule.deleted` | `{id,title}` |
| `schedule.run` | `{plan,claim,error?,manual?}` |
| `agent` | 信封 `{task_id,bot_id,type,time,data}`，见下 |

`agent` 的 `type`：

| type | 持久 | data |
|---|---|---|
| `task.started` | ✅ | `{input,policy,workspace}` |
| `text.delta` | ❌ | `{text}` |
| `thinking.delta` | ❌ | `{text}` |
| `heartbeat` | ❌ | — |
| `tool.call` | ✅ | `{call_id,tool,args}` |
| `tool.result` | ✅ | `{call_id,tool,output,error,denied}` |
| `approval.requested` | ✅ | `{id,task_id,bot_id?,tool_name,args,args_hash,risk,reason,created_at}` |
| `approval.decided` | ✅ | `{approval_id,decision(allow|deny|expired),scope?,decided_by,reason}` |
| `task.paused` | ✅ | `{task_id,status:"paused",steps}` |
| `task.steered` | ✅ | `{text}` |
| `task.completed`/`task.failed`/`task.cancelled` | ✅ | `{task_id,status,output?,steps,error?}` |

纯推送（`seq=0`）：`replay.done{busy}`、`chat.status{busy}`、`chat.delta{text}`、`chat.thinking{text}`、`chat.tool{label}`。

只写盘不推送（Android 收不到，别依赖）：`message(role=notice)`、`message.handled`、`task.notes.used`、`task.reported`、`approval.record`。

### 派生状态（客户端自己算）

- `awaiting`（待审批数）：`approval.requested` +1、`approval.decided` −1（同 id）。
- 任务终态主要由 `task.completed/failed/cancelled` 进入；`task.updated` 也会在「排队中取消 / 启动失败」时直接带 `cancelled`/`failed`（这类任务没有 agent 终态），客户端必须采纳。
- `message.hidden` 出现在重放里视为删除。

---

## 4. 状态词汇

| 维度 | 取值 |
|---|---|
| `task.status` | `queued running paused completed failed cancelled` |
| `stop_reason`（结束） | `user restart` |
| `stop_reason`（暂停） | `user chat restart interrupted held stall` |
| 审批 | `pending allow deny expired`；请求体 `decision`: `once session deny`；`scope`: `once session`；`risk`: `read write exec danger` |
| `Card.status` | `pending answered dismissed` |
| `SecretReq.status` | `pending saved cancelled` |
| 计划 `Claim.status` | `claimed dispatched completed failed needs_verify skipped_misfire skipped_overlap expired` |
| 执行器 kind | `builtin codex pi omp`（任务记录里 builtin 写空串） |
| `cli_auth` | `subscription env channel`（pi 固定 `channel`） |
| 消息 `role` | `user assistant notice` |
| `reaction.by` | `user bot` |

---

## 5. 上限与 JSON 严格性

| 项 | 值 |
|---|---|
| JSON body | 1 MiB |
| 上传单文件 / 整请求 / 文件数 | 25 MiB / 26 MiB / 16 |
| Bot 数量 | 32（含 `main`） |
| 命令行 | 4096 字节 |
| 登录 body | 4 KiB |
| Bot 档案 | 64 KiB |

- 必须是完整 JSON 对象（空 body、裸数组、裸字符串都 400）；`Content-Type` 非 `application/json` → 415。
- 超限 → `413 {"error":"请求内容太大"}`。
- 未知字段允许（向前兼容）。
