# M1 端到端冒烟记录

> 执行日期：2026-11-15 · 分支：`m1-skeleton` · 基线提交：`f5ca6d2`
>
> 本文只记录实际跑过的命令与观察。没有跑到的内容统一列在最后的「未覆盖」里。

## 1. 环境

| 项 | 值 |
|---|---|
| 系统 | macOS（darwin 25.5.0，arm64） |
| Go | `go1.27.3 darwin/arm64` |
| Node / npm | `v24.14.1` / `11.11.0` |
| 数据目录 | `REEVE_HOME=/tmp/reeve-smoke.c6TCCk`（`mktemp -d` 生成） |
| 服务地址 | `127.0.0.1:18800` |
| 假模型 | `cmd/fakeai`，`127.0.0.1:9911`，模型名 `fake-model` |

回环地址访问免认证，所以下面的 `curl` 没有带 token。

## 2. 全量构建与测试

```bash
cd /Users/kiry/code/reeve
go build ./...
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./internal/...
```

实际结果：

- `go build ./...`、`go vet ./...` 均无输出，退出码 0。
- `go test -count=1 ./...` 全部通过：

```
?   github.com/kiry163/reeve/cmd/fakeai         [no test files]
?   github.com/kiry163/reeve/cmd/reeve          [no test files]
?   github.com/kiry163/reeve/internal/atomicfile [no test files]
ok  github.com/kiry163/reeve/internal/auth      0.402s
ok  github.com/kiry163/reeve/internal/bot       5.985s
ok  github.com/kiry163/reeve/internal/builtin   3.552s
ok  github.com/kiry163/reeve/internal/scheduler 2.395s
ok  github.com/kiry163/reeve/internal/vault     0.804s
?   github.com/kiry163/reeve/web                [no test files]
```

- `go test -race -count=1 ./internal/...` 全部通过：

```
ok  github.com/kiry163/reeve/internal/auth      1.542s
ok  github.com/kiry163/reeve/internal/bot      10.114s
ok  github.com/kiry163/reeve/internal/builtin   4.769s
ok  github.com/kiry163/reeve/internal/scheduler 3.149s
ok  github.com/kiry163/reeve/internal/vault     2.852s
```

`go.mod` 仍只有 `module` 与 `go 1.22` 两行，没有引入依赖。

## 3. 前端构建

```bash
cd /Users/kiry/code/reeve/web
npm ci
npm run build
```

实际结果：

- `npm ci`：`added 86 packages in 2s`。
- `npm run build` 走 `tsc --noEmit && vite build`，`✓ built in 766ms`。
- `web/dist/index.html` 存在（704 B）；主资源为 `assets/index-BHScpuOu.js`（362.84 kB）与 `assets/index-HfQTnkS7.css`（75.02 kB）。

## 4. 启动假模型与 reeve

```bash
cd /Users/kiry/code/reeve
go build -o bin/reeve ./cmd/reeve
go build -o bin/fakeai ./cmd/fakeai

REEVE_HOME=$(mktemp -d /tmp/reeve-smoke.XXXXXX)   # 本次为 /tmp/reeve-smoke.c6TCCk
./bin/fakeai                                      # 127.0.0.1:9911
REEVE_HOME="$REEVE_HOME" ./bin/reeve serve -addr 127.0.0.1:18800
```

实际启动输出要点：

```
fakeai 监听 http://127.0.0.1:9911
已生成访问 token（/tmp/reeve-smoke.c6TCCk/token，0600）
已生成主密钥 /tmp/reeve-smoke.c6TCCk/master.key（权限 0600）
reeve dev 已启动
网页：    http://127.0.0.1:18800/
数据：    /tmp/reeve-smoke.c6TCCk
工作区：  /tmp/reeve-smoke.c6TCCk/workspace
```

为了不自动弹浏览器，启动时把 `PATH` 前面放了一个直接退出的假 `open`。两个进程都用工具的服务模式托管，没有用裸 `&`。

## 5. 配置渠道与模型路由

渠道是设备级配置。`POST /api/channels` 的请求体是 `{"providers":[...]}`：

```bash
curl -s -X POST http://127.0.0.1:18800/api/channels \
  -H 'content-type: application/json' \
  -d '{"providers":[{"id":"fake","name":"假模型","base_url":"http://127.0.0.1:9911","enabled":true,"auth_required":false,"models":["fake-model"]}]}'
```

返回（节选）：

```json
{"providers":[{"id":"fake","name":"假模型","base_url":"http://127.0.0.1:9911","enabled":true,"auth_required":false,"has_api_key":false,"models":["fake-model"],"hidden_models":[]}],"usage":[]}
```

Bot 的模型选择在 `POST /api/bots/{id}/settings/routes`，默认 Bot 的 ID 是 `main`：

```bash
curl -s -X POST http://127.0.0.1:18800/api/bots/main/settings/routes \
  -H 'content-type: application/json' \
  -d '{"default_provider_id":"fake","model":"fake-model","agent_provider_id":"fake","agent_model":"fake-model"}'
```

返回里 `default_provider_id`、`agent_provider_id` 均为 `fake`。`GET /api/bots/main/info` 确认（节选）：

```json
{"chat_model":"fake-model","agent_model":"fake-model","agent_cli":"","policy":"untrusted","max_concurrent":3,"stall_sec":90}
```

`GET /api/channels` 的 `usage` 显示 `main` 的聊天与 Agent 都指向 `fake`。

## 6. 发消息 → 聊天层派活 → 执行器干活 → 事件流

发消息和订阅事件流用 Python 标准库完成（便于在收到 `approval.requested` 时自动点一次「允许」）。实际行为等价于：

```bash
curl -sN 'http://127.0.0.1:18800/api/bots/main/stream?after=0' &
curl -s -X POST http://127.0.0.1:18800/api/bots/main/messages \
  -H 'content-type: application/json' -d '{"text":"你好"}'
# 收到 write_file 的 approval.requested 后：
curl -s -X POST http://127.0.0.1:18800/api/bots/main/approvals/appr_36c87475febca359 \
  -H 'content-type: application/json' -d '{"decision":"once"}'
```

`POST /api/messages` 返回用户消息：

```json
{"id":"msg_4b5e5e0609d4c633","role":"user","text":"你好","created_at":"2026-11-15T16:18:19.84587+08:00"}
```

审批请求是 `write_file`，原因是默认 `untrusted` 策略「改文件和执行命令都要先经过你确认」。批准一次后任务继续执行。

### 持久事件序列

SSE 里带 `id:` 的是持久事件；`chat.delta`、`chat.thinking`、`agent` 的增量输出等 seq 为 0，是实时推送，不写事件日志。本次落盘的关键序列：

| seq | kind | 内容 |
|---|---|---|
| 1 | `message` | 用户：`你好` |
| 2 | `message` | 助手：`好，交给后台去做，进度看下面的卡片。` |
| 3 | `task.created` | `task_6b910ebfcd9dbbfa`，标题「你好」 |
| 4 | `agent` | `task.started`，policy=`untrusted`，workspace=`/tmp/reeve-smoke.c6TCCk/workspace` |
| 5 | `message.handled` | internal |
| 6–7 | `agent` | `tool.call`/`tool.result`：`list_dir`，结果 `(空目录)` |
| 8–9 | `agent` | `read_file README.md`，报 `no such file or directory` |
| 10–11 | `agent` | `shell`：`ls -la` 被识别为只读命令（RiskRead），按只读自动放行 |
| 12 | `agent` | `tool.call`：`write_file`，目标 `notes.md` |
| 13 | `agent` | `approval.requested`：`appr_36c87475febca359`，risk=`write` |
| 14–15 | `message` / `approval.record` | 审批通知与审批记录，internal |
| 16 | `agent` | `approval.decided`：allow，scope=`once`，`decided_by=user` |
| 17 | `agent` | `tool.result`：`已写入 notes.md（76 字节）` |
| 18 | `message.handled` | internal |
| 19 | `message` | 助手回复（假模型剧本触发，见下文备注） |
| 20 | `agent` | `task.completed`，status=`completed`，steps=5 |
| 21 | `task.reported` | internal |
| 22 | `task.summary` | 任务 `task_6b910ebfcd9dbbfa` 完成 |
| 23–24 | `message` / `message.handled` | 完成通知与处理标记，internal |
| 25 | `message` | 助手：`搞定了。**工作区概览已写入 notes.md**：里面有 build/（旧构建产物）和 README.md。` |

副作用确认：工作区里生成了 `notes.md`（76 字节），内容为假模型剧本要求的概览。

备注：seq 19 的助手文案是 fakeai 聊天层剧本对「需要用户批准」通知的固定回复，写的是「要删除 build 目录」，但本次实际工具调用是写 `notes.md`。这是假模型剧本的表态与动作不一致，不是产品链路的问题；真实模型不会这样回复。

## 7. 重启恢复

为了在线程运行中 `kill -9`，再发一条消息：

```bash
curl -s -X POST http://127.0.0.1:18800/api/bots/main/messages \
  -H 'content-type: application/json' -d '{"text":"帮我整理一下工作区"}'
```

聊天层又派了一个任务 `task_214aed141bc85659`。在它执行到 `write_file` 并停在 `approval.requested`（`appr_ce983366368fcc82`，此时任务状态为 running、等待审批）时，直接 `kill -9` 服务进程：

```bash
kill -9 54587
```

随后确认 `18800` 已无监听，再用同一个 `REEVE_HOME` 重启，并用 `Last-Event-ID: 0` 拉历史：

```bash
REEVE_HOME="$REEVE_HOME" ./bin/reeve serve -addr 127.0.0.1:18800 &
curl -sN http://127.0.0.1:18800/api/bots/main/stream -H 'Last-Event-ID: 0'
```

实际观察：

- 重放了 **34 个对外可见事件**，seq 从 1 到 46 严格递增，没有重复。
- 缺号（5、14、15、18、21、23、24、30、39、40、44、45）全部是 internal 事件（`message.handled`、任务通知、`approval.record`、`task.reported`），不是丢失；这些本来就不推给客户端。
- seq 42：`approval.decided`，`decision=expired`，`reason=服务重启，审批已失效`。
- seq 43：`task.updated`，`task_214aed141bc85659` 变为 `status=paused`、`stop_reason=restart`。
- seq 44–45：internal 的重启通知与处理标记。
- seq 46：助手：`刚才服务重启，任务暂停了（进度已保存），要继续吗？`

检查点与事件日志：

- 默认 Bot 的数据目录就是 `REEVE_HOME`，所以检查点在 `$REEVE_HOME/checkpoints/task_214aed141bc85659.json`（不是 `$REEVE_HOME/bots/main/checkpoints/`；只有非默认 Bot 才在 `bots/<id>/` 下）。
- 检查点文件存在，内容包含到此为止的 `messages`（user、assistant 的 tool_calls、tool 结果）。恢复续跑本次未实测（见「未覆盖」）。
- `$REEVE_HOME/stream.jsonl` 共 46 条持久事件；重启后追加了 seq 42–46。
- `$REEVE_HOME/workspace/notes.md` 仍存在，证明工作区数据不随进程退出丢失。
- 说明：本节「重启后未决审批变成 `decision=expired`」是修复前的旧行为；当前实现改为重启后**保留**未决审批（仍可作答，session 批准继续生效），审批只在任务结束或服务收到决定后失效。

另外，订阅时也用了 `?after=25` 从指定 seq 之后继续接收，行为与 `Last-Event-ID` 一致（`Last-Event-ID` 优先；两者同时给时以 header 为准，源码见 `internal/bot/server.go` 的 `streamEvents`）。

## 8. 清理

两个托管进程分别停止后检查：

```
port 9911: free
port 18800: free
pgrep -fl '/bin/reeve|bin/fakeai': none
```

`REEVE_HOME` 临时目录保留在 `/tmp/reeve-smoke.c6TCCk` 供复查；不进入仓库。

## 9. 本次发现

没有需要改代码才能跑通链路的缺陷；本次没有改产品代码。

记录到的接口事实（与源码一致，供 M2 参考）：

1. `POST /api/channels` 要包一层 `{"providers":[...]}`；单发一个渠道对象会因「缺少渠道列表」失败。
2. 渠道是设备级配置；Bot 只保存路由，端点是 `POST /api/bots/{id}/settings/routes`。
3. `/api/info` 是 Bot 级端点（`/api/bots/{id}/info`），全局 `/api/info` 返回 404。
4. 默认 Bot 的 `REEVE_HOME` 自身就是数据目录，检查点路径是 `$REEVE_HOME/checkpoints/`。

## 10. 未覆盖

以下内容 M1 没有实现或本次没有跑，不能据此认为正常：

- **环境变量三层**（实例级 / Bot 级 / 调用级）：M2 才实现，本次未覆盖。
- **斜线命令**：M2 才实现，本次未覆盖。
- **技能（SKILL.md）**：M2 才实现，本次未覆盖。
- **外部执行器**（codex / pi / omp 等 CLI）：M3；本次只跑了内置 builtin 执行器。
- **Android 客户端**：M4；本次只验证了 HTTP/SSE 与网页构建。
- 真实第三方模型渠道与真实 API Key：只用了本地 fakeai。
- 多 Bot：只用了默认 Bot `main`。
- 审批的 `session` 与 `deny` 分支：只跑过 `once` 允许。
- 暂停 / 继续 / 取消 / 重试 / 置顶、定时计划、密钥卡片、文件上传、编辑重发、表情回应：本次未逐项点过。
- 任务执行到一半（正在跑工具）时崩溃：本次只在「等待审批」的安全点 `kill -9`，没有在写文件/命令执行中途强杀。
- 网页 UI 的人工点击流程：只验证了 `npm run build` 与静态资源生成，没有用浏览器逐页操作。
- 恢复续跑（重启后让任务从检查点继续执行）：本次只观察到任务暂停与检查点落盘，没有走通续跑。
