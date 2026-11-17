# M3 端到端冒烟记录（外部执行器）

> 执行日期：2026-11-16 · 分支：`m3-external-executors` · 基线提交：`4c1af35`
>
> 下文第 2 节的构建/测试输出录制于基线 `4c1af35`；`84bb42b` 之后前端 `npm test` 共 7 条
> （3 条白名单脏检查 + 4 条任务状态）。
>
> 本文只记录实际跑过的命令与观察。没有跑到的内容统一列在最后的「未覆盖」里。

## 1. 环境

| 项 | 值 |
|---|---|
| 系统 | macOS（darwin 25.5.0，arm64） |
| Go | `go1.27.3 darwin/arm64` |
| Node / npm | `v24.14.1` / `11.11.0` |
| 真实 CLI | `codex-cli 0.169.0`、`omp/18.11.0`；`pi` 未安装 |
| 数据目录 | `/tmp/reeve-m3/homeA`（真实 omp/codex，`127.0.0.1:18860`）、`/tmp/reeve-m3/homeB`（进程控制与重启，`18861`）、`/tmp/reeve-m3/homeC`（并发槽，`18862`）、`/tmp/reeve-m3/homeD`（SIGTERM 复跑，`18863`） |
| 聊天模型 | `/tmp/reeve-m3/stub.py`，OpenAI 兼容桩，`127.0.0.1:9912`，模型 `stub-model`；只在「派活」时返回一次带 `executor` 的 `start_task`，其余请求返回固定文字 |
| 假 omp | `/tmp/reeve-m3/fakebin/omp`（`--version` 输出 `omp/18.11.0`，打印两行合法的 omp JSON 事件后 `sleep`），用于进程控制、重启和并发项；真实 omp 只用于第 4 项 |
| 其他工具脚本 | `/tmp/reeve-m3/openbin/open`（空实现，避免自动弹浏览器）、`/tmp/reeve-m3/dispatch4.sh`（并发派活） |

说明：聊天层派活需要一个模型，而真实模型不可控，所以用一个只实现 OpenAI 聊天协议的桩来触发
`start_task`。服务、调度器、执行器、审批、进程信号、事件日志全部是真实代码；假 omp 只替换了
CLI 进程本身（协议形状与真实 omp 一致）。下文逐条注明哪一步用的是真 CLI、哪一步用的是假 CLI。

## 2. 全量构建与测试

```bash
cd /Users/kiry/code/reeve
go build ./...
go vet ./...
go test -count=1 ./...
go test -race ./internal/...
cd web && npm run build && npm test
```

`go build ./...`、`go vet ./...` 均无输出，退出码 0。测试全部通过：

```
ok  	github.com/kiry163/reeve/cmd/reeve	0.864s
ok  	github.com/kiry163/reeve/internal/auth	1.067s
ok  	github.com/kiry163/reeve/internal/bot	17.081s
ok  	github.com/kiry163/reeve/internal/builtin	2.687s
ok  	github.com/kiry163/reeve/internal/commands	1.728s
ok  	github.com/kiry163/reeve/internal/envstore	1.978s
ok  	github.com/kiry163/reeve/internal/exec	2.122s
ok  	github.com/kiry163/reeve/internal/exec/cli	17.309s
ok  	github.com/kiry163/reeve/internal/scheduler	3.816s
ok  	github.com/kiry163/reeve/internal/skills	3.106s
ok  	github.com/kiry163/reeve/internal/vault	2.655s
```

`go test -race ./internal/...`：

```
ok  	github.com/kiry163/reeve/internal/auth	1.520s
ok  	github.com/kiry163/reeve/internal/bot	20.163s
ok  	github.com/kiry163/reeve/internal/builtin	5.627s
ok  	github.com/kiry163/reeve/internal/commands	3.986s
ok  	github.com/kiry163/reeve/internal/envstore	2.034s
ok  	github.com/kiry163/reeve/internal/exec	4.972s
ok  	github.com/kiry163/reeve/internal/exec/cli	20.213s
ok  	github.com/kiry163/reeve/internal/scheduler	(cached)
ok  	github.com/kiry163/reeve/internal/skills	3.547s
ok  	github.com/kiry163/reeve/internal/vault	1.733s
```

前端：

```
> build
> tsc --noEmit && vite build
✓ 1657 modules transformed.
dist/assets/index-Cut_Eqw_.css     79.99 kB │ gzip:  14.97 kB
dist/assets/index-G1kmcUeP.js     381.96 kB │ gzip: 116.11 kB
✓ built in 848ms

> test
> node --test test/*.test.ts
✔ 白名单顺序变化不算改动
✔ 白名单增删算改动
✔ 默认执行器与 CLI 选项变化算改动
ℹ tests 3
ℹ pass 3
ℹ fail 0
```

## 3. 启动与配置

```bash
cd /Users/kiry/code/reeve
go build -o bin/reeve ./cmd/reeve
mkdir -p /tmp/reeve-m3/homeA
PATH=/tmp/reeve-m3/openbin:$PATH \
  REEVE_HOME=/tmp/reeve-m3/homeA ./bin/reeve serve -addr 127.0.0.1:18860
python3 /tmp/reeve-m3/stub.py          # 127.0.0.1:9912
```

启动输出：

```
已生成访问 token（/tmp/reeve-m3/homeA/token，0600）
已生成主密钥 /tmp/reeve-m3/homeA/master.key（权限 0600）。
reeve dev 已启动
网页：    http://127.0.0.1:18860/
数据：    /tmp/reeve-m3/homeA
工作区：  /tmp/reeve-m3/homeA/workspace
```

把聊天与 Agent 路由指向控制桩：

```bash
curl -s -X POST http://127.0.0.1:18860/api/channels \
  -H 'content-type: application/json' \
  -d '{"providers":[{"id":"stub","name":"控制桩","base_url":"http://127.0.0.1:9912","enabled":true,"auth_required":false,"models":["stub-model"]}]}'

curl -s -X POST http://127.0.0.1:18860/api/bots/main/settings/routes \
  -H 'content-type: application/json' \
  -d '{"default_provider_id":"stub","model":"stub-model","agent_provider_id":"stub","agent_model":"stub-model"}'
```

可用性探测（真实二进制）：

```bash
curl -s http://127.0.0.1:18860/api/settings/connections
```

```json
{"cli":"","clis":[
 {"id":"codex","installed":true,"path":"/opt/homebrew/bin/codex","version":"0.169.0","probed":true,"available":true},
 {"id":"pi","installed":false,"probed":false,"available":false,"note":"找不到 pi，请先安装并放进 PATH"},
 {"id":"omp","installed":true,"path":"/opt/homebrew/bin/omp","version":"18.11.0","probed":true,"available":true}]}
```

（上面为节选，真实响应还含 `name`/`subscription`/`model_option` 等字段。）
`?probe=0` 只刷新安装状态与缓存、不起 `--version`；没有缓存时 `probed=false`，
网页只在 `probed && available===false` 时才判定执行器不可用（单纯 `available=false` 不构成结论）。

`GET /api/bots/main/info` 显示 `max_concurrent=3`、`policy=untrusted`。

## 4. 白名单外派活

初始白名单只有内置执行器：

```bash
curl -s http://127.0.0.1:18860/api/bots/main/executors
# {"allowed":["builtin"],"default":"builtin","clis":{}}
```

斜线命令按 kind 切换默认执行器，被白名单拦住：

```bash
curl -s -X POST http://127.0.0.1:18860/api/bots/main/commands \
  -H 'content-type: application/json' -d '{"line":"/agent omp"}'
# {"text":"切换执行器失败：omp 不在白名单里，当前可用：builtin；先用设置页把它加入白名单","ok":false}
```

聊天层派活（控制桩返回 `start_task` 且 `executor=omp`）同样被拒。工具结果由控制桩记录到请求日志：

```
执行器 omp 不在这个 Bot 的白名单里（可用：builtin）。换一个可用执行器，或让用户先在设置里把它加进白名单。
```

同一时刻 `stream.jsonl` 中 `approval` 与 `task.created` 均为 0 条，`/tasks` 返回：

```json
{"text":"没有符合条件的任务。","ok":true}
```

**断言：白名单外派活被拒、提示白名单、不弹审批卡、不建任务 → ok。**

## 5. 派活前审批

把 omp 加入白名单：

```bash
curl -s -X PUT http://127.0.0.1:18860/api/bots/main/executors \
  -H 'content-type: application/json' \
  -d '{"allowed":["builtin","omp"],"default":"builtin","clis":{}}'
# {"allowed":["builtin","omp"],"default":"builtin","clis":{}}
```

再让聊天层派一个 `executor=omp` 的任务，`stream.jsonl` 只出现一张审批卡（seq 11）：

```json
{"seq":11,"kind":"agent","data":{"task_id":"chat_187711ac7c54b8c2","type":"approval.requested",
 "data":{"id":"appr_9c0deceeac724c25","tool_name":"start_task",
 "args":{"executor":"omp","task_hash":"f27813…","title":"真实 omp 冒烟","preview":"冒烟真实任务：执行 pwd && echo omp-ok"},
 "risk":"exec","reason":"当前模式下，改文件和执行命令都要先经过你确认"}}}
```

批准前：

```bash
grep -c 'task.created' /tmp/reeve-m3/homeA/stream.jsonl
# 0
```

批准一次：

```bash
curl -s -X POST http://127.0.0.1:18860/api/bots/main/approvals/appr_9c0deceeac724c25 \
  -H 'content-type: application/json' -d '{"decision":"once"}'
# {"ok":true}
```

批准后立刻写 `task.created`（带 `executor=omp`）并开始运行：

```json
{"seq":17,"kind":"task.created","data":{"id":"task_515cf029d0ee1e8a","title":"真实 omp 冒烟",
 "input":"冒烟真实任务：执行 pwd && echo omp-ok","executor":"omp","status":"queued"}}
{"seq":19,"kind":"agent","data":{"task_id":"task_515cf029d0ee1e8a","type":"task.started",
 "data":{"workspace":"/tmp/reeve-m3/homeA/workspace"}}}
```

**断言：一张审批卡；批准前无 `task.created`；批准后任务带 `executor=omp` → ok。**

> 注：本轮没有 `GET /api/bots/main/tasks` 端点（实际返回 404）；任务列表用 `/tasks` 斜线命令，
> 状态流转用 `$REEVE_HOME/stream.jsonl`。`/tasks` 也是网页任务列表的数据来源（SSE 重放同一批事件）。

## 6. 真实 omp 任务

任务原文：`冒烟真实任务：执行 pwd && echo omp-ok`，工作区 `/tmp/reeve-m3/homeA/workspace`。
同一时间用 `curl -sN /api/bots/main/stream` 抓实时事件。

实时 SSE 里共 73 条 `text.delta`（`text.delta` 只推给界面、不落盘，见 `emitExecEvent`）：

```json
data: {"seq":0,"kind":"agent","data":{"task_id":"task_8d297c46be4eeb1e","type":"text.delta",
 "data":{"text":"命令"}}}
```

`stream.jsonl` 里的工具与终态（真实 omp 调用 bash 工具）：

```json
{"seq":67,"kind":"agent","data":{"task_id":"task_8d297c46be4eeb1e","type":"tool.call",
 "data":{"tool":"bash","args":{"command":"pwd && echo omp-ok"}}}}
{"seq":68,"kind":"agent","data":{"task_id":"task_8d297c46be4eeb1e","type":"tool.result",
 "data":{"tool":"bash","output":"/tmp/reeve-m3/homeA/workspace\nomp-ok","error":false}}}
{"seq":69,"kind":"agent","data":{"task_id":"task_8d297c46be4eeb1e","type":"task.completed",
 "data":{"status":"completed","steps":1,
 "output":"命令已执行，两个部分都返回了输出：\n\n```\n/tmp/reeve-m3/homeA/workspace\nomp-ok\n```\n\n…冒烟测试通过。"}}}
```

**断言：流式 `text.delta` 出现；终态 `completed`；输出含工作区绝对路径与 `omp-ok` → ok。**
执行器标识就是任务记录里的 `"executor":"omp"`（网页任务卡片的徽标读同一字段）。

## 7. 真实 codex 任务

本机 codex 的上游网关仍不可用。直接跑：

```bash
codex exec --json --skip-git-repo-check "Reply with the single word: pong"
```

```
{"type":"thread.started","thread_id":"01a1dff7-…"}
{"type":"error","message":"Reconnecting... 1/5 (unexpected status 502 Bad Gateway: Unknown error, url: http://127.0.0.1:8100/responses)"}
…
{"type":"error","message":"Reconnecting... 4/5 (unexpected status 502 Bad Gateway: Unknown error, url: http://127.0.0.1:8100/responses)"}
```

命令最终被 90 秒超时终止（exit 124）。经 reeve 派同一任务（`executor=codex`，批准一次）后，
任务在约 119 秒后失败：

```json
{"seq":41,"kind":"agent","data":{"task_id":"task_019633f125408c56","type":"task.failed",
 "data":{"status":"failed","steps":0,
 "error":"unexpected status 502 Bad Gateway: Unknown error, url: http://127.0.0.1:8100/responses"}}}
{"seq":44,"kind":"message","data":{"role":"notice",
 "text":"任务「真实 codex 冒烟」(task_019633f125408c56) 失败了。原因：unexpected status 502 Bad Gateway: Unknown error, url: http://127.0.0.1:8100/responses。请如实告诉用户哪里出了问题，并给出下一步建议。"}}
```

**断言：失败文案如实透出（502 与网关地址），任务终态 `failed`，不误报完成 → ok（记为「成功路径未覆盖」）。**

## 8. 进程控制（假 omp 长任务）

`/tmp/reeve-m3/fakebin/omp` 打印合法 omp 事件后 `sleep 30`。派活并批准后：

```bash
ps -o pid,ppid,pgid,stat,command -ax | grep '[f]akebin/omp'
# 71428 71131 71428 S /bin/sh /tmp/reeve-m3/fakebin/omp -p --mode=json …
```

暂停：

```bash
curl -s -X POST http://127.0.0.1:18861/api/bots/main/tasks/task_394ea3bebce41e8a/pause
# {"message":"已暂停：外部执行器的进程已挂起，继续时会从原处接着跑。"}
ps -o pid,pgid,stat,command -ax | grep '[f]akebin/omp'
# 71428 71428 T /bin/sh /tmp/reeve-m3/fakebin/omp -p --mode=json …
```

恢复：

```bash
curl -s -X POST http://127.0.0.1:18861/api/bots/main/tasks/task_394ea3bebce41e8a/resume
# {"message":"已继续，外部执行器从挂起处接着跑。"}
ps -o pid,pgid,stat,command -ax | grep '[f]akebin/omp'
# 71428 71428 S /bin/sh /tmp/reeve-m3/fakebin/omp -p --mode=json …
```

取消：

```bash
curl -s -X POST http://127.0.0.1:18861/api/bots/main/tasks/task_394ea3bebce41e8a/cancel
# {"message":"已停止。"}
```

第一次轮询（约 0.1 秒）进程就已消失，随后事件日志：

```json
{"kind":"task.updated","data":{"id":"task_394ea3bebce41e8a","stop_reason":"user"}}
{"kind":"agent","data":{"task_id":"task_394ea3bebce41e8a","type":"task.cancelled",
 "data":{"status":"cancelled"}}}
```

**断言：暂停 `ps` 含 `T`；恢复含 `S`；取消后 5 秒内退出（实测 <0.2 秒）；`cancelled` + `stop_reason=user` → ok。**
（`<0.2 秒` 是假 CLI 立即响应 `TERM` 的结果；真实 CLI 忽略 `TERM` 时的 5 秒宽限与 `SIGKILL` 升级未实测，见 §11。）
`task.cancelled` 的 `data` 不含 `stop_reason`，所以同一份证据引用紧随其前的 `task.updated{stop_reason:"user"}`。

## 9. 重启

### 9.1 `SIGTERM`（正常停机）

> 复跑日期：2026-11-16 00:25（修复「优雅停机把外部任务记成 cancelled」之后）。数据目录 `/tmp/reeve-m3/homeD`，端口 `18863`，`PATH` 前置 `/tmp/reeve-m3/fakebin`，聊天走控制桩。任务 `task_46d186184145387d`（`executor=omp`，假 omp 打印两行事件后 `sleep`）。

任务运行中 `ps` 能看到假 CLI：

```bash
ps -o pid,ppid,pgid,stat,command -ax | grep '[f]akebin/omp'
# 77311 77280 77311 S /bin/sh /tmp/reeve-m3/fakebin/omp -p --mode=json --no-session --no-skills --no-rules --max-time 1800 当前任务：…
```

`kill -TERM <reeve-pid>`：reeve 输出 `正在退出…` 后退出（轮询 `kill -0`，0.1 秒粒度内观察到进程消失），子进程一并消失、`ps` 无残留。`stream.jsonl` 里该任务的全部状态事件：

```json
{"seq":10,"kind":"task.created","time":"2026-11-16T00:25:36.524475+08:00","data":{"id":"task_46d186184145387d","title":"SIGTERM 中断冒烟","input":"SIGTERM 中断冒烟：打印后保持运行","executor":"omp","status":"queued",…}}
{"seq":12,"kind":"agent","time":"2026-11-16T00:25:36.530519+08:00","data":{"task_id":"task_46d186184145387d","type":"task.started",…}}
{"seq":15,"kind":"task.updated","time":"2026-11-16T00:25:36.895459+08:00","data":{"id":"task_46d186184145387d","status":"paused","stop_reason":"interrupted",…}}
```

```bash
grep -c '"type":"task.cancelled"' /tmp/reeve-m3/homeD/stream.jsonl
# 0
grep -c '用户取消' /tmp/reeve-m3/homeD/stream.jsonl
# 0
```

用同一 `REEVE_HOME` 重启后 `/tasks`：

```bash
curl -s -X POST http://127.0.0.1:18863/api/bots/main/commands \
  -H 'content-type: application/json' -d '{"line":"/tasks"}'
# {"text":"共 1 个任务，显示最近 1 个（新的在后）：\n- task_46d186184145387d [paused] 「SIGTERM 中断冒烟」 — 服务重启，外部执行器任务已中断，不会自动重跑（0 步，用时 1 秒）\n","ok":true}
```

重启前后 `task.created` 与 `task.started` 的条数都保持 1（`grep -c` 各一次），没有重复派发或续跑。

**断言：SIGTERM 停机把运行中的外部任务落 `paused/interrupted`、子进程消失、无 `task.cancelled` 与“用户取消”通知；重启后仍 `interrupted` 且不重跑 → ok。**
只有用户主动 cancel（§8）才产生 `cancelled + stop_reason=user`；内置任务停机仍按取消收尾。

### 9.2 `SIGKILL`（异常退出）

任务运行中 `kill -9 <reeve-pid>`：reeve 立即消失，外部进程组成为孤儿（符合 spec 的已知边界）：

```
72848 1 72848 S /bin/sh /tmp/reeve-m3/fakebin/omp -p --mode=json …
```

用同一 `REEVE_HOME` 重启后：

```bash
curl -s -X POST http://127.0.0.1:18861/api/bots/main/commands \
  -H 'content-type: application/json' -d '{"line":"/tasks"}'
# - task_7976ae74923bfe9c [paused] 「重启中断冒烟2」 — 服务重启，外部执行器任务已中断，不会自动重跑（0 步，用时 10 秒）
```

事件与通知文案：

```json
{"seq":82,"kind":"task.updated","data":{"id":"task_7976ae74923bfe9c","status":"paused","stop_reason":"interrupted"}}
{"seq":83,"kind":"message","data":{"role":"notice",
 "text":"服务重启了，这些外部执行器任务已中断（不会自动重跑，需要请重新派一次）：「重启中断冒烟2」(task_7976ae74923bfe9c)。简短告诉用户它被中断了、没有自动重跑，需要的话用 start_task 按原要求重新派一次。"}}
```

重启后没有任何该任务的 `task.started`（`grep` 计数 0）。

**断言：异常重启后 `paused/interrupted`；无自动重跑；通知文案正确 → ok。**
孤儿进程由人工 `kill -TERM -<pgid>` 清理，随后 `ps` 无残留。

## 10. 并发槽

`max_concurrent=3`。连续派 4 个假 omp 任务（每个 `sleep 20`），逐一批准。第 4 个批准后立刻查询：

```json
{"text":"共 4 个任务，显示最近 4 个（新的在后）：
- task_ac191aa4f9254f7d [running] 「并发冒烟」 — 运行中，已 0 步
- task_6c5ede0253a42dcd [running] 「并发冒烟」 — 运行中，已 0 步
- task_f790cd874263e2fb [running] 「并发冒烟」 — 运行中，已 0 步
- task_c294340bc429508e [queued] 「并发冒烟4」 — 排队第 1 位，有空位自动开始（0 步，用时 0 秒）","ok":true}
```

等待全部结束（约 40 秒）后：

```json
{"text":"共 4 个任务，显示最近 4 个（新的在后）：
- task_ac191aa4f9254f7d [completed] 「并发冒烟」 — fake-omp-start（0 步，用时 20 秒）
- task_6c5ede0253a42dcd [completed] 「并发冒烟」 — fake-omp-start（0 步，用时 20 秒）
- task_f790cd874263e2fb [completed] 「并发冒烟」 — fake-omp-start（0 步，用时 20 秒）
- task_c294340bc429508e [completed] 「并发冒烟4」 — fake-omp-start（0 步，用时 20 秒）","ok":true}
```

`ps` 无 fake omp 子进程。再派第 5 个任务，它立即 `[running]` 而不是 `[queued]`，证明空位已释放：

```json
- task_35870bdbe1813518 [running] 「槽归零验证」 — 运行中，已 0 步（0 步，用时 2 秒）
```

**断言：前 3 个并行、第 4 个排队第 1 位；结束后全部终态、无残留进程；新任务立即开始 → ok。**
并发槽计数没有对外端点（`/api/info` 只有 `max_concurrent`），所以用「排队位置 + 结束后立即开跑 + 无子进程」三项间接证明归零。

## 11. 未覆盖

| 项 | 原因 |
|---|---|
| 真实 `pi` 执行器 | 本机未安装（`probed:false`、`available:false`，`note:"找不到 pi，请先安装并放进 PATH"`） |
| 真实 codex 成功路径 | 上游网关 `127.0.0.1:8100` 持续 502，只能记录失败样本 |
| 网页交互（点按钮暂停/审批/白名单编辑） | 本次只走 HTTP API 与 SSE；网页构建与前端单测通过，但没有浏览器点击记录 |
| Windows 上的暂停/恢复/终止 | 只在代码层确认返回 `ErrSignalUnsupported`（`taskkill /T /F` 终止），没有 Windows 机器实测 |
| 卡死检测（外部 10 分钟无输出） | 需要 10 分钟窗口，本次未跑 |
| 忽略 `TERM` 时的 5 秒宽限与 `SIGKILL` 升级 | 假 omp 立即响应 `TERM`，升级链路未实测 |
| 真实长任务上的暂停/恢复 | 用假 omp 验证（进程控制与 CLI 无关）；真实 omp 只跑了 2 秒的最小任务 |
| `GET /api/bots/main/tasks` | 该端点不存在（404），改用 `/tasks` 命令与 `stream.jsonl` |

## 12. 清理确认

所有进程与端口已关闭：

```bash
lsof -nP -iTCP:18860 -iTCP:18861 -iTCP:18862 -iTCP:18863 -iTCP:9912 -sTCP:LISTEN
# no listeners
ps -o pid,stat,command -ax | grep -E '[b]in/reeve serve|[f]akebin/omp|[s]tub.py'
# no leftovers
```

数据目录 `/tmp/reeve-m3/homeA|homeB|homeC|homeD`、控制桩与脚本保留在 `/tmp/reeve-m3/` 供复查，
不进入仓库；`bin/reeve`、`bin/fakeai` 是本地构建产物，未提交。
