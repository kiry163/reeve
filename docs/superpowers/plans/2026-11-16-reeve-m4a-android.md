# Reeve M4a：Android 客户端 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 `android/` 交付一个能连上 reeve 的 Android 客户端：配对（扫码/粘贴/手填 + `reeve://pair` 深链）、聊天与任务、审批与卡片、命令面板、环境变量与执行器/技能设置、前台保活与本地通知。

**Architecture:** 在既有 Kotlin/Compose 客户端骨架上裁剪，删掉 reeve 没有的 relay/自更新/root/多设备，把网络层从「用户名密码 + Cookie + `/api/devices/...`」改成「Bearer token + `/api/bots/{id}`」。事件归并抽成**纯函数**（`EventReducer`）以便 JVM 单测；UI 仍用自写导航栈 + `AppState` 单例，不引入 ViewModel/Hilt。

**Tech Stack:** Kotlin 2.0.21、AGP 8.7.3、Gradle 8.11.1、compileSdk/targetSdk 35、minSdk 26、Compose BOM 2024.12.01、OkHttp 4.12、kotlinx-coroutines、ZXing embedded（扫码）、JUnit4（单测）。

**Spec:** `docs/superpowers/specs/2026-11-14-reeve-design.md` §16（认证/端点/Android）；**契约以 `docs/android-contract.md` 为准**。

## Global Constraints

- 包名/命名空间 `dev.reeve.app`；`applicationId` 同；`app_name` = `reeve`；`rootProject.name = "reeve"`。
- 鉴权一律 `Authorization: Bearer <token>`；**不发** `Origin` 头；401 统一提示「token 失效，请重新配对」并把服务器标记为需要重新配对。
- 客户端只依赖 `docs/android-contract.md` 的端点；不新增服务端接口（需要时先停下报告）。
- 不引入 Google Play Services 依赖（扫码用 ZXing embedded，离线可用）。
- 删除不需要的子系统：relay、自更新、root 提权、多设备、CLI 一键安装。
- 明文 HTTP 允许（局域网），但 `res/xml/network_security_config.xml` 必须保留；说明文字保留「仅限可信局域网」。
- UI 文案中文；与网页术语一致（任务/审批/执行器/技能/环境变量）。
- 单测跑 JVM：`./gradlew --no-daemon :app:testDebugUnitTest`；构建：`./gradlew --no-daemon :app:assembleDebug`。
- 本机工具链：`JAVA_HOME=/opt/homebrew/opt/openjdk@17`、`ANDROID_HOME=/opt/homebrew/share/android-commandlinetools`（SDK 35 + build-tools 35.0.0 已装）。

---

### Task 1: 骨架落地与构建通过

**Files:**
- Create: `android/`（在既有 Compose 骨架上裁剪）
- Delete: `android/app/src/main/java/.../core/Relay.kt`、`core/ClientUpdater.kt`
- Delete: `android/app/src/main/res/xml/update_file_paths.xml`
- Modify: `android/settings.gradle.kts`、`android/app/build.gradle.kts`、`android/app/src/main/AndroidManifest.xml`、`android/app/src/main/res/values/strings.xml`、`android/app/src/main/res/values/themes.xml`、`android/app/src/main/java/dev/reeve/app/**`（包名与 import）
- Test: `./gradlew :app:assembleDebug`

**Interfaces:**
- Produces: 可编译的 `dev.reeve.app` 骨架（本任务不保证功能，只保证构建）

- [ ] **Step 1: 建立工程骨架**

```bash
cd /Users/kiry/code/reeve
mkdir -p android/app/src/main/java/dev/reeve/app
cd android
# settings.gradle.kts: rootProject.name = "reeve"
# app/build.gradle.kts: namespace 与 applicationId = dev.reeve.app
```

- [ ] **Step 2: 删掉不需要的代码与构建步骤**

```bash
rm -f app/src/main/java/dev/reeve/app/core/Relay.kt \
      app/src/main/java/dev/reeve/app/core/ClientUpdater.kt \
      app/src/main/res/xml/update_file_paths.xml
# 删除 build.gradle.kts 里的资源复制 task 与两条 assets/res srcDir
# 删除 buildConfigField("boolean","OFFICIAL_UPDATES",…)
# AndroidManifest：删 UpdateFileProvider 与 FILE_PROVIDER 相关；保留 INTERNET / FOREGROUND_SERVICE /
#   POST_NOTIFICATIONS / networkSecurityConfig；application android:label="@string/app_name"
```

同时把引用 `Relay`/`ClientUpdater`/`PrivilegeSection` 的 UI 分支删掉（`App.kt`、`SettingsScreens.kt`、`AppState.kt` 会一并报错，逐个删）。

- [ ] **Step 3: 图标与品牌资源**

```bash
# 自备启动图标：沿用旧图标不合适，改用纯色 + 文字占位（M4 不做美术）
mkdir -p app/src/main/res/mipmap-anydpi-v26 app/src/main/res/drawable
# 生成 adaptive icon：background = #1F7A5A，foreground = vector "R"
# monochrome 一并给；同时在 values/strings.xml 写 app_name=reeve
# themes.xml 主题名改 Theme.Reeve，Theme.kt 同步
```

- [ ] **Step 4: 构建**

Run: `cd android && JAVA_HOME=/opt/homebrew/opt/openjdk@17 ANDROID_HOME=/opt/homebrew/share/android-commandlinetools ./gradlew --no-daemon :app:assembleDebug`
Expected: `BUILD SUCCESSFUL`；产物 `app/build/outputs/apk/debug/app-debug.apk`

- [ ] **Step 5: 提交**

```bash
git add android
git commit -m "feat(android): 初始化客户端骨架"
```

---

### Task 2: 网络层与鉴权

**Files:**
- Modify: `android/app/src/main/java/dev/reeve/app/core/ServerClient.kt`
- Modify: `android/app/src/main/java/dev/reeve/app/core/AppState.kt`（服务器存储：`host` + `token`）
- Test: `android/app/src/test/java/dev/reeve/app/ServerConfigTest.kt`

**Interfaces:**
- Consumes: `docs/android-contract.md` §1、§2.1
- Produces:
  - `ServerConfig(id, name, url, token)`；`normalize()` 提供协议补全
  - `ServerClient.get/post/put/patch/delete(path, body)`、`stream(path, lastSeq, onFrame)`、`upload(files)`
  - `ApiException(status, message)`；401 → `needsPairing`
  - `health(): Health`、`capabilities(): Caps`、`me(): AuthMe`

- [ ] **Step 1: 单测（先失败）**

```kotlin
class ServerConfigTest {
    @Test fun normalizesBareHostWithPort() {
        assertEquals("http://192.168.1.5:18800", ServerConfig.normalize("192.168.1.5:18800"))
    }
    @Test fun keepsHttpsForDomain() {
        assertEquals("https://reeve.example.com", ServerConfig.normalize("reeve.example.com"))
    }
    @Test fun stripsTrailingSlash() {
        assertEquals("http://host:18800", ServerConfig.normalize("http://host:18800/"))
    }
    @Test fun parsesPairUri() {
        val c = ServerConfig.fromPairUri("reeve://pair?v=1&host=192.168.1.5:18800&token=abc")
        assertEquals("http://192.168.1.5:18800", c.url)
        assertEquals("abc", c.token)
    }
    @Test fun rejectsWrongSchemeOrVersion() {
        assertNull(ServerConfig.fromPairUri("other://pair?v=1&host=x&token=y"))
        assertNull(ServerConfig.fromPairUri("reeve://pair?v=2&host=x&token=y"))
        assertNull(ServerConfig.fromPairUri("reeve://pair?host=x"))
    }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd android && ./gradlew :app:testDebugUnitTest --tests '*ServerConfigTest*'`
Expected: FAIL（`fromPairUri` 未定义）

- [ ] **Step 3: 实现**

- 删 `username`/`login()`/`COOKIE`；`request()` 加 `header("Authorization", "Bearer ${cfg.token}")`。
- `ServerConfig` 增加 `companion object { fun normalize(raw: String): String; fun fromPairUri(uri: String): ServerConfig? }`（用 `android.net.Uri` 或 `java.net.URI`；单测走 JVM 时避免依赖 Android 类 → 用 `java.net.URI` 手写解析）。
- 所有错误统一 `ApiException`：`status` + 后端 `{"error":…}` 文案；401 时抛 `NeedsPairing`。
- `AppState` 的 `servers` 持久化改为 `{id,name,url,token}`；`sessions` 不再使用（Bearer 无 Cookie）；提供 `replaceToken(id, token)` 供重新配对。

- [ ] **Step 4: 测试与构建**

Run: `./gradlew :app:testDebugUnitTest :app:assembleDebug`
Expected: 全绿

- [ ] **Step 5: 提交**

```bash
git add android
git commit -m "feat(android): Bearer 鉴权与配对串解析"
```

---

### Task 3: 模型与事件归并（纯函数 + 单测）

**Files:**
- Modify: `android/app/src/main/java/dev/reeve/app/core/Models.kt`
- Create: `android/app/src/main/java/dev/reeve/app/core/EventReducer.kt`
- Test: `android/app/src/test/java/dev/reeve/app/EventReducerTest.kt`

**Interfaces:**
- Consumes: `docs/android-contract.md` §3、§4
- Produces:
  - `data class BotState(items: List<Item>, msgs: Map<String, Msg>, tasks: Map<String, BotTask>, approvals: Map<String, Approval>, cards: Map<String, Card>, secrets: Map<String, SecretReq>, busy: Boolean, streaming: String, thinking: String, lastSeq: Long, ready: Boolean)`
  - `EventReducer.apply(state, ev: WireEvent): BotState`（纯函数，返回新状态）
  - `WireEvent(seq: Long, kind: String, time: String, data: JSONObject)`
  - `fun deriveAwaiting(state): Unit` 由 reducer 内部维护
  - `Item` 密封接口：`MessageItem(id)`、`TaskItem(id)`、`NoticeItem(id)`（`command.result` 合成）

- [ ] **Step 1: 单测（先失败）**

```kotlin
class EventReducerTest {
    private fun ev(seq: Long, kind: String, data: String) = WireEvent(seq, kind, "", JSONObject(data))

    @Test fun replaysMessagesAndTasksInOrder() { /* message → task.created → task.completed */ }

    @Test fun ignoresDuplicateAndOldSeq() {
        var s = EventReducer.empty()
        s = EventReducer.apply(s, ev(5, "message", """{"id":"m1","role":"user","text":"hi","created_at":""}"""))
        s = EventReducer.apply(s, ev(5, "message", """{"id":"m2","role":"user","text":"dup","created_at":""}"""))
        assertEquals(1, s.msgs.size)
    }

    @Test fun terminalStatusComesOnlyFromAgentEvents() {
        var s = EventReducer.empty()
        s = EventReducer.apply(s, ev(1, "task.created", """{"id":"t1","title":"x","input":"x","status":"queued","created_at":""}"""))
        s = EventReducer.apply(s, ev(2, "agent", """{"task_id":"t1","type":"task.started","data":{}}"""))
        assertEquals("running", s.tasks["t1"]!!.status)
        s = EventReducer.apply(s, ev(3, "agent", """{"task_id":"t1","type":"task.completed","data":{"status":"completed","output":"ok"}}"""))
        assertEquals("completed", s.tasks["t1"]!!.status)
    }

    @Test fun approvalCountTracksRequestedAndDecided() {
        var s = EventReducer.empty()
        s = EventReducer.apply(s, ev(1, "agent", """{"task_id":"t1","type":"approval.requested","data":{"id":"a1","tool_name":"shell","args":{},"risk":"exec","reason":"x"}}"""))
        assertEquals(1, s.tasks["t1"]!!.awaiting)
        s = EventReducer.apply(s, ev(2, "agent", """{"task_id":"t1","type":"approval.decided","data":{"approval_id":"a1","decision":"allow"}}"""))
        assertEquals(0, s.tasks["t1"]!!.awaiting)
    }

    @Test fun hiddenRemovesMessage() {
        var s = EventReducer.empty()
        s = EventReducer.apply(s, ev(1, "message", """{"id":"m1","role":"user","text":"hi"}"""))
        s = EventReducer.apply(s, ev(2, "message.hidden", """["m1"]"""))
        assertTrue(s.msgs.isEmpty())
    }

    @Test fun commandResultBecomesNoticeItem() {
        var s = EventReducer.empty()
        s = EventReducer.apply(s, ev(1, "command.result", """{"command":"/help","text":"…","ok":true}"""))
        assertEquals(1, s.items.count { it is Item.NoticeItem })
    }

    @Test fun taskUpdatedClearStopReasonWins() {
        var s = EventReducer.empty()
        s = EventReducer.apply(s, ev(1, "task.created", """{"id":"t1","title":"x","input":"x","status":"paused","stop_reason":"user","created_at":""}"""))
        s = EventReducer.apply(s, ev(2, "task.updated", """{"id":"t1","status":"running","clear_stop_reason":true}"""))
        assertNull(s.tasks["t1"]!!.stopReason)
        assertEquals("running", s.tasks["t1"]!!.status)
    }

    @Test fun executorSurvivesReplay() {
        var s = EventReducer.empty()
        s = EventReducer.apply(s, ev(1, "task.created", """{"id":"t1","title":"x","input":"x","status":"queued","executor":"omp","created_at":""}"""))
        assertEquals("omp", s.tasks["t1"]!!.executor)
    }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `./gradlew :app:testDebugUnitTest --tests '*EventReducerTest*'`
Expected: FAIL

- [ ] **Step 3: 实现 `EventReducer.apply`**

必须逐条处理契约 §3 的每个 `kind`；`agent.text.delta/thinking.delta/heartbeat` 只改 `streaming/thinking`，不改 `lastSeq`（`seq=0` 事件本来就不带 seq）；`replay.done` 置 `ready=true` 并记录 `busy`；`message` 的 `hidden=true` 按删除处理（先加后删的场景由 `message.hidden` 保证）。

- [ ] **Step 4: 测试通过**

Run: `./gradlew :app:testDebugUnitTest`
Expected: 全绿（含 Task 2 的 ServerConfigTest）

- [ ] **Step 5: 提交**

```bash
git add android
git commit -m "feat(android): 事件归并纯函数与模型"
```

---

### Task 4: BotSession 接线（SSE + 动作调用）

**Files:**
- Modify: `android/app/src/main/java/dev/reeve/app/core/BotSession.kt`
- Modify: `android/app/src/main/java/dev/reeve/app/core/AppState.kt`
- Test: 单测覆盖 `BotSession.frame()`（SSE 解析）与重连退避计算

**Interfaces:**
- Consumes: `EventReducer`、`ServerClient`、契约 §2.2–§2.5
- Produces:
  - `BotSession.state: MutableState<BotState>`（Compose）
  - `BotSession.send(text, files)`、`stopAll()`、`stopReply()`、`regenerate()`、`edit(id, text)`、`react(id, emoji)`
  - `taskAction(id, action)`、`approve(id, decision)`、`answerCard(id, answer?, dismiss)`、`saveSecret(...)`、`cancelSecretRequest(id)`
  - `runCommand(line)`、`setExecutors(json)`、`saveEnv(scope, vars)`、`loadSkills()`、`loadCommands()`
  - `refresh()`（`GET B/info` + `/api/bots` + `/api/settings`）

- [ ] **Step 1: SSE 解析单测（先失败）**

```kotlin
class SseFrameTest {
    @Test fun parsesIdAndData() {
        val f = BotSession.parseFrame(listOf("id: 42", "data: {\"seq\":42,\"kind\":\"message\",\"data\":{}}"))
        assertEquals(42L, f!!.seq)
        assertEquals("message", f.kind)
    }
    @Test fun ignoresCommentsAndEmpty() {
        assertNull(BotSession.parseFrame(listOf(": ping")))
        assertNull(BotSession.parseFrame(listOf("data:")))
    }
    @Test fun treatsSeqZeroAsVolatile() {
        val f = BotSession.parseFrame(listOf("data: {\"seq\":0,\"kind\":\"chat.delta\",\"data\":{\"text\":\"x\"}}"))
        assertEquals(0L, f!!.seq)
    }
}
```

- [ ] **Step 2: 实现**

- 连接：`GET B/stream`，带 `Last-Event-ID: <lastSeq>`（当 `lastSeq>0`），无 `Last-Event-ID` 时退回 `?after=`；读到 `data:` 行解析 JSON → `EventReducer.apply`。
- 重连：`delay(min(2000 * 2^attempt, 30000))`；成功后 `attempt=0`；401 → `markNeedsPairing` 并停。
- 所有动作端点按契约实现；错误弹 Snackbar/Note（中文文案直接用后端 `error`）。
- `send` 支持 `files`（Task 2 的 `upload` 先传附件）。

- [ ] **Step 3: 测试与构建**

Run: `./gradlew :app:testDebugUnitTest :app:assembleDebug`
Expected: 全绿

- [ ] **Step 4: 提交**

```bash
git add android
git commit -m "feat(android): SSE 会话与动作调用接线"
```

---

### Task 5: 聊天界面适配

**Files:**
- Modify: `android/app/src/main/java/dev/reeve/app/ui/ChatScreen.kt`、`ui/Cards.kt`、`ui/Fmt.kt`、`ui/App.kt`
- Test: 手工（模拟器，见 Task 11）；`Fmt` 的状态文案加单测

**Interfaces:**
- Consumes: `BotState`
- Produces: 与网页一致的渲染规则

- [ ] **Step 1: 渲染规则**

- `Item.NoticeItem`（`command.result`）：居中、等宽小字、失败用警告色；无操作按钮。
- 任务卡：状态（`queued/running/paused/completed/failed/cancelled`）+ `stop_reason` 文案；`interrupted` 显示「已中断，需要重新执行」且动作为 `retry`；外部执行器显示徽标（`omp/codex/pi`，`builtin` 不显示）。
- 审批卡：显示 `tool_name`、`risk`、`reason`、`args`（JSON 格式化，含 `preview` 时优先展示为正文预览），三个动作 `once/session/deny`。
- 工具步骤：`tool.call`/`tool.result` 折叠行；`error/denied` 用警告色。
- 卡片（`ask_user`）：选项按钮 + 自由输入；已回答/已作废只读。
- 密钥卡：只显示 name/description，输入框提交后显示「已保存」。
- 流式：`streaming` 与 `thinking` 两区，`replay.done` 之前不显示「完成」。

- [ ] **Step 2: Fmt 单测**

```kotlin
class FmtTest {
    @Test fun stopReasonText() {
        assertEquals("等你补充", Fmt.pauseReason("user"))
        assertEquals("已中断，需要重新执行", Fmt.pauseReason("interrupted"))
    }
    @Test fun taskStatusText() {
        assertEquals("已完成", Fmt.taskStatus("completed"))
    }
}
```

- [ ] **Step 3: 构建 + 提交**

Run: `./gradlew :app:testDebugUnitTest :app:assembleDebug`

```bash
git add android
git commit -m "feat(android): 聊天与任务界面适配 reeve 事件"
```

---

### Task 6: 命令面板

**Files:**
- Create: `android/app/src/main/java/dev/reeve/app/ui/CommandPanel.kt`
- Modify: `ui/ChatScreen.kt`（输入框 `/` 触发 + 按钮入口）
- Test: 单测 `CommandFilterTest`（名字/别名/摘要过滤，大小写不敏感）

**Interfaces:**
- Consumes: `GET /api/commands`、`POST B/commands`
- Produces: `CommandPanel(commands, onRun: (String) -> Unit)`

- [ ] **Step 1: 行为**

- 输入框内容以 `/` 开头时，上方弹出候选（≤8 条，按 `name`/`aliases`/`summary` 过滤）；点击或回车执行：非空参数时 `runCommand(整行)`，仅命令名时先追加空格。
- 结果由 `command.result` 事件渲染成 notice 行，不阻塞输入。
- 面板顶部显示「命令直接生效，不走模型」提示。

- [ ] **Step 2: 测试与构建**

Run: `./gradlew :app:testDebugUnitTest :app:assembleDebug`

- [ ] **Step 3: 提交**

```bash
git add android
git commit -m "feat(android): 命令面板"
```

---

### Task 7: 设置 — 环境变量（实例级 / Bot 级）

**Files:**
- Create: `android/app/src/main/java/dev/reeve/app/ui/EnvScreen.kt`
- Modify: `ui/SettingsScreens.kt`（入口）、`ui/App.kt`（路由）

**Interfaces:**
- Consumes: `GET/PUT /api/env`、`GET/PUT B/env`
- Produces: 两级列表 + 编辑 + 整体替换保存 + `warnings` 展示

- [ ] **Step 1: 行为**

- 两个分区：实例级（所有 Bot 共用）与当前 Bot 级；每行 name/value/删除；新增行。
- 保存 = 读当前表 → 合并 → 整层 PUT；显示 `{"ok":true,"warnings":[…]}` 的 warnings；失败原样显示 `error`。
- 顶部提示：「保存会用当前表格整体替换本层变量」。
- 空表时给一句说明（`REEVE_HOME/env` 文件位置）。

- [ ] **Step 2: 构建 + 提交**

```bash
git add android
git commit -m "feat(android): 环境变量两级编辑"
```

---

### Task 8: 设置 — 执行器白名单 / 技能索引 / 渠道与模型

**Files:**
- Create: `android/app/src/main/java/dev/reeve/app/ui/ExecutorsScreen.kt`
- Create: `android/app/src/main/java/dev/reeve/app/ui/SkillsScreen.kt`
- Modify: `ui/SettingsScreens.kt`（渠道/模型改用全局端点）、`ui/App.kt`

**Interfaces:**
- Consumes: `GET/PUT B/executors`、`GET B/skills`、`GET/POST /api/settings`、`GET/POST /api/channels`、`GET /api/settings/connections?probe=0`
- Produces: 三块设置页

- [ ] **Step 1: 执行器**

- `builtin` 恒选且不可取消（说明「内置执行器始终可用」）。
- 外部项显示 `installed/version/probed/available/note`；`probed && available==false` 时禁用勾选（已勾选的可取消）。
- 勾选外部项显示警告：「外部 CLI 等于把整台机器的执行权交给它」。
- 默认执行器单选只列白名单；保存 400 时原样显示错误。
- 每个已勾选 CLI 可改 `cli_model`/`cli_auth`（`pi` 固定 `channel`，只改模型）。

- [ ] **Step 2: 技能**

只读列表：`name`、`description`、`when_to_use`、`path`；`errors` 用警告色；空态说明 `skills/<name>/SKILL.md`。

- [ ] **Step 3: 渠道与模型**

复用既有的 `ChannelsScreen`/`ChannelEditor` 表单骨架，端点改为全局 `/api/settings` 与 `/api/channels`；删除 bot 级 `saveRoutes` 以外多余入口（路由仍可用 `POST B/settings/routes`）。

- [ ] **Step 4: 构建 + 提交**

```bash
git add android
git commit -m "feat(android): 执行器、技能与全局渠道设置"
```

---

### Task 9: 配对

**Files:**
- Create: `android/app/src/main/java/dev/reeve/app/ui/PairScreen.kt`
- Modify: `AndroidManifest.xml`（`reeve://pair` intent-filter + CAMERA 权限）、`MainActivity.kt`（处理 `ACTION_VIEW`）、`SettingsScreens.kt`（添加入口）、`AppState.kt`（保存/替换 token）
- Test: `PairUriTest`（Task 2 已覆盖解析；本任务补「重复添加同一 host 复用条目」）

**Interfaces:**
- Consumes: `ServerConfig.fromPairUri`、`GET /api/health`
- Produces: 三种入口 —— 扫码、粘贴串、手填 host+token

- [ ] **Step 1: 行为**

- 手填：host（`IP:端口` 或域名）+ token 两个输入框；保存前 `GET /api/health` 校验可达、`GET /api/auth/me` 校验 token（`authenticated=true` 或 `mode=loopback`）。
- 粘贴：从剪贴板读 `reeve://pair?...`，解析后预填。
- 扫码：ZXing embedded 的 `CaptureActivity`，扫到 `reeve://` 串后预填；导入 `com.journeyapps:zxing-android-embedded:4.3.0`（**唯一新增依赖**）。
- 深链：`ACTION_VIEW` 收到 `reeve://pair` 直接进入预填页（不自动保存）。
- 重复 host：已存在则更新 token 并保留 Bot 偏好。
- 明文 HTTP 警告保留：「reeve 没有 TLS，请只在可信局域网使用；跨网络请用 Tailscale 或 SSH 隧道」。

- [ ] **Step 2: 构建 + 提交**

```bash
git add android
git commit -m "feat(android): 配对（扫码/粘贴/手填/深链）"
```

---

### Task 10: 保活与通知

**Files:**
- Modify: `KeepAliveService.kt`、`ReeveApp.kt`、`AppState.kt`（通知触发点）、`SettingsScreens.kt`（通知开关）

**Interfaces:**
- Consumes: `BotState` 变化
- Produces: 前台服务 + 三类通知

- [ ] **Step 1: 行为**

- 通知渠道：`alerts`（审批，HIGH）、`replies`（助手回复，DEFAULT）、`keepalive`（MIN、无角标）。
- 触发：`approval.requested` → 「有一个操作等你确认」；`task.completed/failed/cancelled` → 「任务「x」完成了/失败了」；新助手消息（且在后台）→ 摘要。
- 前台且正在看该 Bot 时抑制；三类各有开关（持久化）。
- 通知点击：`PendingIntent` 带 `serverId|botId`，`MainActivity` 打开对应聊天。
- 前台服务文案：「reeve 正在保持连接」。

- [ ] **Step 2: 构建 + 提交**

```bash
git add android
git commit -m "feat(android): 前台保活与本地通知"
```

---

### Task 11: 模拟器验证与文档

**Files:**
- Create: `docs/android.md`（安装/配对/手工验证清单/已知限制）
- Create: `docs/images/android-*.png`（截图，≤3 张）

- [ ] **Step 1: 起模拟器**

```bash
export ANDROID_HOME=/opt/homebrew/share/android-commandlinetools JAVA_HOME=/opt/homebrew/opt/openjdk@17
echo no | avdmanager create avd -n reeve -k "system-images;android-35;google_apis;arm64-v8a" -d pixel_6 --force
$ANDROID_HOME/emulator/emulator -avd reeve -no-window -no-audio -no-snapshot -gpu swiftshader_indirect &
adb wait-for-device
```

- [ ] **Step 2: 端到端**

```bash
# 服务端：假模型 + 临时数据目录，监听 0.0.0.0 让模拟器可达
go build -o /tmp/reeve-android ./cmd/reeve
REEVE_HOME=$(mktemp -d /tmp/reeve-android.XXXXXX) /tmp/reeve-android serve -addr 0.0.0.0:18880 &
# 模拟器里用 10.0.2.2:18880 访问宿主机
adb install -r android/app/build/outputs/apk/debug/app-debug.apk
adb shell am start -a android.intent.action.VIEW -d "reeve://pair?v=1&host=10.0.2.2:18880&token=$(cat $REEVE_HOME/token)"
```

手工清单（逐条截图或记录输出）：
1. 配对成功 → 会话列表出现 `main`；
2. 发「你好」→ 收到助手流式回复（假模型）；
3. 输入 `/help` → 弹出候选 → 执行 → 聊天里出现居中 notice 行；
4. 设置 → 环境变量：加一个变量 → 保存 → `/env list` 能看到；
5. 设置 → 执行器：勾选 omp（若宿主 PATH 里有）→ 保存 → 默认改 omp → 派任务时出现外部执行器徽标；
6. 设置 → 技能：列出 `data/skills` 下的示例技能；
7. 后台时任务完成 → 收到通知，点击进入对应聊天；
8. 杀掉服务 → 客户端显示「正在重新连接」；恢复服务后自动续传（`Last-Event-ID`）。

- [ ] **Step 3: 文档与提交**

`docs/android.md` 写清：如何构建 APK、`10.0.2.2` 的用法、真机连接（局域网 IP + token）、已知限制（不做系统推送、不自更新、不迁移旧数据）。

```bash
git add docs/android.md docs/images
git commit -m "docs: Android 安装与手工验证清单"
```

---

## 自审记录

- **Spec 覆盖**：§16.3（前台保活 + 本地通知、设置页三块、命令面板）、§16.1（配对串/Bearer）、§7（事件归并）全部有任务。
- **明确不做**：系统推送、应用内自更新、多设备/relay、root 提权、旧数据迁移。
- **类型一致性**：`BotState`/`Item`/`BotTask`/`Approval`/`Card`/`SecretReq`/`ServerConfig`/`WireEvent` 在 Task 2–10 同名；`EventReducer.apply` 是唯一事件入口。
- **风险**：Task 9 引入 ZXing 依赖（唯一新增）；Task 11 的模拟器在无 GPU 环境需 `swiftshader`，首次启动可能 2–4 分钟；若模拟器不可用，Task 11 只落地「构建 + 单测 + 手工清单」，并在文档里明确未在设备上验证。
