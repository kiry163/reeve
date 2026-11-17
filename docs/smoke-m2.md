# M2 端到端冒烟记录（环境变量 / 斜线命令 / 技能）

> 执行日期：2026-11-15 · 分支：`m2-env-commands-skills` · 基线提交：`bd7568f`
>
> 本文只记录实际跑过的命令与观察。没跑到的内容统一列在最后的「未覆盖」里。

## 1. 环境

| 项 | 值 |
|---|---|
| 系统 | macOS（darwin 25.5.0，arm64） |
| Go | `go1.27.3 darwin/arm64` |
| Node / npm | `v24.14.1` / `11.11.0` |
| 数据目录 | `REEVE_HOME=/tmp/reeve-smoke-m2.QwQJRg`（`mktemp -d` 生成） |
| 服务地址 | `127.0.0.1:18800` |
| 假模型 | `cmd/fakeai`，`127.0.0.1:9911`，模型名 `fake-model` |
| 技能文件 | `$REEVE_HOME/skills/demo/SKILL.md`（启动前写入，启动时加载） |

回环地址访问免认证，所以下面的 `curl` 没有带 token。启动时把 `PATH` 前面放了一个直接退出的假 `open`，避免自动弹浏览器；`fakeai` 与 `reeve` 都由工具的服务模式托管。

### 关于本次用的假模型

`cmd/fakeai` 的剧本是写死的：后台 agent 只会依次调用 `list_dir`、`read_file README.md`、`shell`（`ls -la`）、`write_file`，聊天层也没有 `read_skill` 分支。因此它**无法**执行 `printf %s "$REEVE_SMOKE"`，也无法触发 `read_skill`。

为了在真实服务里走通这两条路径，本次额外在仓库外写了一个最小的 OpenAI 兼容记录桩（`/tmp/reeve-m2-stub/stub.py`，没有进仓库）：

- 记录每个请求的完整 JSON 到 `/tmp/reeve-m2-stub/requests.jsonl`；
- 收到带 `start_task` 工具的聊天请求时，按用户文本决定返回 `start_task` 或 `read_skill`，否则返回一句固定文字；
- 后台 agent 请求第一次返回 `shell` 调用 `printf %s "$REEVE_SMOKE"`，之后返回最终文字。

它只是模型协议的替身，服务、调度器、执行器、审批、事件全部是真实代码。下文逐条注明哪一步用的是 `fakeai`、哪一步用的是记录桩；没有观察到的内容不会写成已完成。

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
?   	github.com/kiry163/reeve/cmd/fakeai	[no test files]
ok  	github.com/kiry163/reeve/cmd/reeve	0.691s
?   	github.com/kiry163/reeve/internal/atomicfile	[no test files]
ok  	github.com/kiry163/reeve/internal/auth	0.994s
ok  	github.com/kiry163/reeve/internal/bot	10.172s
ok  	github.com/kiry163/reeve/internal/builtin	5.149s
ok  	github.com/kiry163/reeve/internal/commands	1.846s
ok  	github.com/kiry163/reeve/internal/envstore	2.146s
ok  	github.com/kiry163/reeve/internal/scheduler	3.739s
ok  	github.com/kiry163/reeve/internal/skills	2.740s
ok  	github.com/kiry163/reeve/internal/vault	3.350s
?   	github.com/kiry163/reeve/web	[no test files]
```

- `go test -race -count=1 ./internal/...` 全部通过：

```
?   	github.com/kiry163/reeve/internal/atomicfile	[no test files]
ok  	github.com/kiry163/reeve/internal/auth	1.520s
ok  	github.com/kiry163/reeve/internal/bot	13.109s
ok  	github.com/kiry163/reeve/internal/builtin	6.906s
ok  	github.com/kiry163/reeve/internal/commands	2.900s
ok  	github.com/kiry163/reeve/internal/envstore	2.471s
ok  	github.com/kiry163/reeve/internal/scheduler	5.195s
ok  	github.com/kiry163/reeve/internal/skills	3.442s
ok  	github.com/kiry163/reeve/internal/vault	4.889s
```

前端：

```bash
cd /Users/kiry/code/reeve/web
npm run build
```

`tsc --noEmit && vite build` 通过，`✓ built in 799ms`。产物：

```
dist/index.html                    704 B
dist/assets/index-BQAJ0dqr.css     80.59 kB │ gzip: 15.03 kB
dist/assets/index-DvdmsLIz.js     375.46 kB │ gzip: 114.42 kB
```

`go.mod` 仍只有 `module` 与 `go 1.22` 两行，没有引入依赖。

## 3. 启动与配置

```bash
cd /Users/kiry/code/reeve
go build -o bin/reeve ./cmd/reeve
go build -o bin/fakeai ./cmd/fakeai

REEVE_HOME=$(mktemp -d /tmp/reeve-smoke-m2.XXXXXX)   # 本次为 /tmp/reeve-smoke-m2.QwQJRg
mkdir -p "$REEVE_HOME/skills/demo"                    # SKILL.md 见 4.4，启动前写入

./bin/fakeai                                          # 127.0.0.1:9911
REEVE_HOME="$REEVE_HOME" ./bin/reeve serve -addr 127.0.0.1:18800
```

启动输出要点：

```
fakeai 监听 http://127.0.0.1:9911
已生成访问 token（/tmp/reeve-smoke-m2.QwQJRg/token，0600）
已生成主密钥 /tmp/reeve-smoke-m2.QwQJRg/master.key（权限 0600）
reeve dev 已启动
网页：    http://127.0.0.1:18800/
数据：    /tmp/reeve-smoke-m2.QwQJRg
工作区：  /tmp/reeve-smoke-m2.QwQJRg/workspace
```

配置渠道与路由（`POST /api/channels` 要包一层 `{"providers":[...]}`；后来为了 4.1 / 4.4 又加了一个指向记录桩的渠道）：

```bash
curl -s -X POST http://127.0.0.1:18800/api/channels \
  -H 'content-type: application/json' \
  -d '{"providers":[{"id":"fake","name":"假模型","base_url":"http://127.0.0.1:9911","enabled":true,"auth_required":false,"models":["fake-model"]},{"id":"stub","name":"记录桩","base_url":"http://127.0.0.1:9912","enabled":true,"auth_required":false,"models":["stub-model"]}]}'

curl -s -X POST http://127.0.0.1:18800/api/bots/main/settings/routes \
  -H 'content-type: application/json' \
  -d '{"default_provider_id":"fake","model":"fake-model","agent_provider_id":"fake","agent_model":"fake-model"}'
```

路由返回里聊天与 Agent 都指向 `fake`；`GET /api/bots/main/info` 显示 `chat_model`、`agent_model` 均为 `fake-model`，policy 为 `untrusted`。

## 4. 冒烟项

### 4.1 Bot 级环境变量进入执行器（记录桩）

先写入 Bot 级变量：

```bash
curl -s -X PUT http://127.0.0.1:18800/api/bots/main/env \
  -H 'content-type: application/json' -d '{"vars":{"REEVE_SMOKE":"hello"}}'
# {"ok":true,"warnings":[]}
curl -s http://127.0.0.1:18800/api/bots/main/env
# {"vars":{"REEVE_SMOKE":"hello"}}
```

把聊天 / Agent 路由临时切到记录桩（`default_provider_id`、`agent_provider_id` 均为 `stub`），然后发消息派任务：

```bash
curl -s -X POST http://127.0.0.1:18800/api/bots/main/messages \
  -H 'content-type: application/json' -d '{"text":"ENVSMOKE 请执行环境变量检查"}'
```

记录桩聊天层返回 `start_task`，任务 `task_dbc70f393b3eb6fa` 的输入是「执行 printf %s "$REEVE_SMOKE" 并报告输出」。Agent 第一次请求返回 `shell` 调用，因 policy=`untrusted` 停在审批：

事件 seq 8（节选）：

```json
{"seq":8,"kind":"agent","data":{"type":"approval.requested","data":{"id":"appr_bad49395559e356e","task_id":"task_dbc70f393b3eb6fa","tool_name":"shell","args":{"command":"printf %s \"$REEVE_SMOKE\""},"risk":"exec","reason":"当前模式下，改文件和执行命令都要先经过你确认"}}}
```

批准一次：

```bash
curl -s -X POST http://127.0.0.1:18800/api/bots/main/approvals/appr_bad49395559e356e \
  -H 'content-type: application/json' -d '{"decision":"once"}'
# {"ok":true}
```

执行结果（`$REEVE_HOME/stream.jsonl` seq 15，真实输出）：

```json
{"seq":15,"kind":"agent","data":{"type":"tool.result","tool":"shell","denied":false,"error":false,"output":"hello\n[exit code 0]"}}
```

seq 16 `task.completed`，status=`completed`，output=`冒烟任务完成。`。

**断言：任务输出含 `hello` → ok。** 这证明 Bot 级变量在任务创建时进入合并环境，并被 `shell` 子进程实际读到。

### 4.2 `/env list` 命令与「不产生用户消息事件」

```bash
curl -s -X POST http://127.0.0.1:18800/api/bots/main/commands \
  -H 'content-type: application/json' -d '{"line":"/env list"}'
```

返回：

```json
{"text":"bot 级：\n  REEVE_SMOKE=hello","ok":true}
```

命令执行前后 `stream.jsonl` 从 21 行（seq 21）变为 22 行，只新增了：

```json
{"seq":22,"kind":"command.result","data":{"command":"/env","ok":true,"text":"bot 级：\n  REEVE_SMOKE=hello"}}
```

**断言：返回含 `REEVE_SMOKE`，落盘有 `command.result`，没有新增 `message`（用户消息）事件 → ok。**

### 4.3 实例级环境变量与 `/env set`

```bash
curl -s -X PUT http://127.0.0.1:18800/api/env \
  -H 'content-type: application/json' -d '{"vars":{"INST_ONLY":"x"}}'
# {"ok":true,"warnings":[]}
curl -s http://127.0.0.1:18800/api/env
# {"vars":{"INST_ONLY":"x"}}

curl -s -X POST http://127.0.0.1:18800/api/bots/main/commands \
  -H 'content-type: application/json' -d '{"line":"/env list"}'
# {"text":"实例级（--global）：\n  INST_ONLY=x\nbot 级：\n  REEVE_SMOKE=hello","ok":true}

curl -s -X POST http://127.0.0.1:18800/api/bots/main/commands \
  -H 'content-type: application/json' -d '{"line":"/env set K=V"}'
# {"text":"已设置 K（bot 级）。","ok":true}

curl -s http://127.0.0.1:18800/api/bots/main/env
# {"vars":{"K":"V","REEVE_SMOKE":"hello"}}
```

`stream.jsonl` 同时出现 seq 23 `env.set`（`layer=instance`，`name=INST_ONLY`，`source=user`）与 seq 25 `env.set`（`layer=bot`，`name=K`，`source=user`），事件里没有值。

**断言：实例级变量出现在 `/env list`；`/env set K=V` 后 `GET /api/env`（bot 级）能看到 `K` → ok。**

### 4.4 技能索引、`GET /api/skills` 与 `read_skill`

启动前放入：

```markdown
---
name: demo
description: 冒烟用的示例技能
when_to_use: 用户要在冒烟里验证技能加载时
---

# Demo 技能

第一行正文：demo-skill-body-line。
```

`GET /api/bots/main/skills` 的真实返回：

```json
{"skills":[{"name":"demo","description":"冒烟用的示例技能","when_to_use":"用户要在冒烟里验证技能加载时","path":"skills/demo/SKILL.md"}],"errors":[]}
```

`path` 是相对标签 `skills/demo/SKILL.md`，没有泄漏绝对路径。`/skills` 命令返回：

```json
{"text":"可用技能：\n- demo","ok":true}
```

聊天层系统提示的证据来自记录桩捕获的真实请求体（`requests.jsonl` 里带 `start_task` 工具的那条请求，`messages[0].role=system`）：

```
## 技能索引
demo — 冒烟用的示例技能 — 用户要在冒烟里验证技能加载时

需要时用 read_skill 读取正文，不要在回答里复述整篇技能。
```

同一条请求的 `tools` 里包含 `read_skill`。随后让记录桩按索引发出 `read_skill {"name":"demo"}`，工具结果（记录桩捕获的下一条请求里的 `role=tool` 消息）为：

```
# Demo 技能

第一行正文：demo-skill-body-line。

（技能文件：skills/demo/SKILL.md）
```

助手最终文本：`已按索引读取技能正文（见工具结果）。`

**断言：技能被加载、端点返回且路径为相对标签、聊天提示词含技能索引、`read_skill` 能按名字读出正文 → ok。**

说明：`read_skill` 这一条是用记录桩触发的，`cmd/fakeai` 的写死剧本没有 `read_skill` 分支，无法用它触发；记录桩走的是真实聊天工具执行链路。

### 4.5 `/help` 与非法行

```bash
curl -s -X POST http://127.0.0.1:18800/api/bots/main/commands \
  -H 'content-type: application/json' -d '{"line":"/help"}'
```

返回 `ok=true`，依次列出 10 条命令：`/help`、`/stop`、`/clear`、`/model`、`/agent`、`/env`、`/skills`、`/context`、`/tasks`、`/approve`，每条都带一句说明与用法。

```bash
curl -s -o /tmp/reeve-m2-hi.json -w '%{http_code}\n' \
  -X POST http://127.0.0.1:18800/api/bots/main/commands \
  -H 'content-type: application/json' -d '{"line":"hi"}'
```

实际返回 HTTP `400`，响应体：

```json
{"error":"line 必须以 / 开头，例如 /env list"}
```

**断言：`/help` 返回 10 条帮助；非命令返回 400 → ok。**

### 4.6 `/clear`

发一条消息并等助手回复：

```bash
curl -s -X POST http://127.0.0.1:18800/api/bots/main/messages \
  -H 'content-type: application/json' -d '{"text":"clear 冒烟测试"}'
# {"id":"msg_4f98189eb1345059","role":"user","text":"clear 冒烟测试",...}
```

`stream.jsonl` 新增 seq 31（用户消息）、seq 32 `message.handled`、seq 33（助手：`收到，这是冒烟用的固定回复。`）。然后：

```bash
curl -s -X POST http://127.0.0.1:18800/api/bots/main/commands \
  -H 'content-type: application/json' -d '{"line":"/clear"}'
# {"text":"已清空当前会话上下文（旧消息已隐藏，历史文件仍保留）。","ok":true}
```

新增事件：

```json
{"seq":34,"kind":"message.hidden","data":["msg_4f98189eb1345059","msg_b876ca6fe03ebdfa"]}
{"seq":35,"kind":"command.result","data":{"command":"/clear","ok":true,"text":"已清空当前会话上下文（旧消息已隐藏，历史文件仍保留）。"}}
```

服务没有消息列表端点：`GET /api/bots/main/messages` 返回 `405 Method Not Allowed`（只有 `POST`），顶层 `GET /api/messages` 返回 404。因此按网页同款方式重放事件流判定可见消息：把所有 `message` 事件的 id 减去 `message.hidden` 里的 id，结果 `message events: 8 | hidden ids: 8 | visible: 0`。`Last-Event-ID: 0` 重放最后以 `replay.done` 结束，没有报错。

**断言：`/clear` 后重放事件流，可见消息为 0 → ok。**

## 5. 本次发现

没有需要改产品代码才能跑通的缺陷；本次没有改产品代码（只新增本文档）。

记录到的接口事实：

1. `POST /api/channels` 要包一层 `{"providers":[...]}`；渠道是设备级配置，Bot 只保存路由。
2. `PUT /api/env` 是实例级（所有 Bot 共用），`PUT /api/bots/{id}/env` 是 Bot 级；两者都要求显式传 `vars`，`{"vars":{}}` 表示清空。
3. 消息相关端点都是 Bot 级：`POST /api/bots/{id}/messages` 可用，顶层 `POST /api/messages` 返回 404；没有消息列表 GET。
4. 技能只在启动时加载；冒烟必须在启动 `reeve serve` 之前放好 `$REEVE_HOME/skills/<name>/SKILL.md`，没有 reload 端点。
5. 命令结果只写 `command.result` 事件，不进聊天消息、不进模型上下文；`/env list` 的普通变量值会进入该事件，token / 主密钥 / vault 值仍被脱敏。

## 6. 未覆盖

以下内容本次没有跑，不能据此认为正常：

- **网页 UI 的人工点击流程**：只验证了 `npm run build` 与接口，没有用浏览器逐页操作环境变量面板、技能面板、命令结果行。
- **真实第三方模型渠道与真实 API Key**：只用了 `cmd/fakeai` 与本地记录桩。
- **`/model`、`/agent` 的切换效果**：只出现在 `/help` 列表里，没有实际切换并观察后续请求。
- **`/stop`、`/approve` 命令、`/tasks`、`/context`**：本次没有逐条执行。
- **模型发起的 `set_env` 走审批**：只验证了用户命令直接生效与 shell 的审批链路，没有让模型主动改环境变量。
- **技能的加载错误分支**（缺字段、重名、不可读）：只验证了正常技能；单测覆盖，但本次没在真实服务里放坏技能。
- **多 Bot**：只用了默认 Bot `main`。
- **Android 客户端**：M4；本次只验证 HTTP/SSE 与网页构建。
- **`fakeai` 原生剧本触发 `read_skill`**：做不到（剧本没有该分支）；本次用记录桩覆盖了该工具链路，但没有真实模型自主选择技能的行为。

## 7. 清理

服务与记录桩全部停止后检查：

```
port 9911: free
port 9912: free
port 18800: free
pgrep -fl 'bin/reeve|bin/fakeai|reeve-m2-stub': none
```

`REEVE_HOME` 临时目录保留在 `/tmp/reeve-smoke-m2.QwQJRg` 供复查（含 `stream.jsonl`、`workspace`、`skills/`），不进入仓库；记录桩与请求日志在 `/tmp/reeve-m2-stub/`，同样不进入仓库。
