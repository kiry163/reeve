package dev.reeve.app.core

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import okhttp3.Request
import org.json.JSONArray
import org.json.JSONObject
import org.json.JSONTokener
import java.util.UUID

enum class NewsKind { Reply, Task, Approval }

data class PendingUpload(
    val id: String = UUID.randomUUID().toString(),
    val name: String,
    val size: Long,
    val attachment: Attachment? = null,
    val error: String? = null,
) {
    val uploading: Boolean get() = attachment == null && error == null
}

/** 一帧解析后的 SSE：id 行 / data.seq 给出持久位点，data 是整条 WireEvent 的 JSON。 */
data class SseFrame(val seq: Long, val kind: String, val time: String, val data: Any)

/**
 * 一个 Bot 的实时状态：连着它的 SSE 事件流，把帧交给 [EventReducer] 归并成 [BotState]。
 * 断线后优先用 `Last-Event-ID` 续传（服务端不支持时退回 `?after=`），失败按指数退避重连。
 */
class BotSession(
    val ref: BotRef,
    profile: BotProfile,
    val server: ServerEntry,
    private val scope: CoroutineScope,
) {
    var profile by mutableStateOf(profile)

    /** 由 reducer 生成的唯一状态副本；UI 只读。 */
    var state by mutableStateOf(BotState())
        private set

    // 旧 UI（Task 5 之前）的读取面，全部从 state 派生。
    val items: List<TimelineItem>
        get() = state.items.map {
            when (it) {
                is Item.MessageItem -> TimelineItem(ItemKind.Msg, it.id)
                is Item.NoticeItem -> TimelineItem(ItemKind.Msg, it.id)
                is Item.TaskItem -> TimelineItem(ItemKind.Task, it.id)
                is Item.ApprovalItem -> TimelineItem(ItemKind.Approval, it.id)
            }
        }
    val msgs: Map<String, Msg> get() = state.msgs
    val tasks: Map<String, BotTask> get() = state.tasks
    val approvals: Map<String, Approval> get() = state.approvals
    val busy: Boolean get() = state.busy
    val streaming: String get() = state.streaming
    val thinking: String get() = state.thinking
    val toolLabel: String get() = state.toolLabel
    val ready: Boolean get() = state.ready
    val scheduleRevision: Int get() = state.scheduleRevision

    var connected by mutableStateOf(false)
    var info by mutableStateOf<BotInfo?>(null)
    var settings by mutableStateOf<ApiSettings?>(null)
        private set

    var draft by mutableStateOf("")
    val uploads = mutableStateListOf<PendingUpload>()

    var onNews: ((BotSession, String, NewsKind) -> Unit)? = null

    private var job: Job? = null

    val client: ServerClient get() = server.client

    val basePath: String get() = "/api/bots/${ref.bot}"

    // 派生状态
    val running: List<BotTask> get() = tasks.values.filter { it.status == TaskStatus.running }
    val queued: List<BotTask> get() = tasks.values.filter { it.status == TaskStatus.queued }.sortedBy { it.prio }
    val paused: List<BotTask> get() = tasks.values.filter { it.status == TaskStatus.paused }
    val working: Boolean get() = tasks.values.any { it.isLive }
    val pendingApprovals: Int get() = approvals.values.count { it.status == "pending" }
    val openTasks: Int get() = tasks.values.count { it.isOpen }
    val queuePositions: Map<String, Int> get() = queued.mapIndexed { i, t -> t.id to i + 1 }.toMap()
    val stallMs: Double get() = minOf((info?.stallSec ?: 720) * 1000.0 / 2, 180_000.0)

    val lastMessage: Msg?
        get() = state.items.asReversed().firstNotNullOfOrNull {
            if (it is Item.MessageItem) state.msgs[it.id] else null
        }

    val regenerateId: String?
        get() {
            if (busy) return null
            val visible = state.items.filterIsInstance<Item.MessageItem>().mapNotNull { state.msgs[it.id] }
            val lastUser = visible.lastOrNull { it.role == "user" } ?: return null
            val lastReply = visible.lastOrNull { it.role == "assistant" } ?: return null
            return if (lastReply.created >= lastUser.created) lastReply.id else null
        }

    // ---------- 事件流 ----------

    fun start() {
        if (job?.isActive == true) return
        job = scope.launch {
            if (info == null) launch { loadInfo() }
            var attempt = 0
            while (isActive) {
                when (val end = streamOnce()) {
                    End.Unauthorized -> {
                        connected = false
                        server.markNeedsPairing()
                        return@launch
                    }
                    is End.Dropped -> {
                        connected = false
                        if (end.opened) attempt = 0
                        delay(retryDelayMs(attempt))
                        attempt++
                    }
                }
            }
        }
    }

    fun stop() {
        job?.cancel()
        job = null
        connected = false
    }

    private sealed interface End {
        data object Unauthorized : End
        data class Dropped(val opened: Boolean) : End
    }

    private suspend fun streamOnce(): End = withContext(Dispatchers.IO) {
        val last = state.lastSeq
        val req: Request = try {
            val builder = client.request("$basePath/stream").header("Accept", "text/event-stream")
            if (last > 0) {
                builder.header("Last-Event-ID", last.toString()).header("Cache-Control", "no-cache")
            } else {
                builder.header("Cache-Control", "no-cache")
            }
            builder.get().build()
        } catch (_: Exception) {
            return@withContext End.Dropped(false)
        }
        val call = ServerClient.stream.newCall(req)
        var opened = false
        try {
            call.execute().use { resp ->
                if (resp.code == 401) return@withContext End.Unauthorized
                if (resp.code != 200) return@withContext End.Dropped(false)
                val source = resp.body?.source() ?: return@withContext End.Dropped(false)
                opened = true
                withContext(Dispatchers.Main) { connected = true }
                val frame = ArrayList<String>()
                while (isActive) {
                    val line = source.readUtf8Line() ?: break
                    if (line.isEmpty()) {
                        if (frame.isNotEmpty()) {
                            dispatch(frame)
                            frame.clear()
                        }
                    } else {
                        frame.add(line)
                    }
                }
                if (frame.isNotEmpty()) dispatch(frame)
            }
        } catch (_: Exception) {
            // 断线：外层循环按退避重连，续传位点在 state.lastSeq。
        } finally {
            call.cancel()
        }
        End.Dropped(opened)
    }

    private suspend fun dispatch(lines: List<String>) {
        val frame = parseFrame(lines) ?: return
        withContext(Dispatchers.Main) { applyFrame(frame) }
    }

    private fun applyFrame(frame: SseFrame) {
        val before = state
        val ev = WireEvent(frame.seq, frame.kind, frame.time, frame.data)
        val after = EventReducer.apply(before, ev)
        if (after === before) return
        state = after
        if (before.ready) announce(before, after, ev)
    }

    /** 重放期间（before.ready=false）不提醒；实时事件按状态差量提醒。 */
    private fun announce(before: BotState, after: BotState, ev: WireEvent) {
        after.approvals.forEach { (id, a) ->
            if (a.status == "pending" && before.approvals[id]?.status != "pending") news(APPROVAL_NOTICE, NewsKind.Approval)
        }
        after.msgs.forEach { (id, m) ->
            val old = before.msgs[id]
            if (old == null && m.role == "assistant" && m.text.isNotEmpty()) news(m.text, NewsKind.Reply)
        }
        after.tasks.forEach { (id, t) ->
            val old = before.tasks[id]
            if (t.status.terminal && old != null && !old.status.terminal) {
                val verb = when (t.status) {
                    TaskStatus.completed -> "完成了"
                    TaskStatus.failed -> "失败了"
                    else -> "已取消"
                }
                news(taskNotice(t.title, verb), NewsKind.Task)
            }
        }
        if (ev.kind == "schedule.run") {
            val d = ev.data as? JSONObject ?: return
            val status = d.obj("claim").str("status")
            if (status == "needs_verify" || status == "failed" || status == "expired") {
                news("定时计划「${d.obj("plan").str("title")}」：$status", NewsKind.Task)
            }
        }
    }

    private fun news(text: String, kind: NewsKind) {
        onNews?.invoke(this, text, kind)
    }

    // ---------- 操作 ----------

    private suspend fun <T> guarded(block: suspend () -> T): T = try {
        block()
    } catch (e: NeedsPairing) {
        server.markNeedsPairing()
        throw e
    }

    suspend fun loadInfo() {
        runCatching { BotInfo.from(client.getObject("$basePath/info")) }.onSuccess { info = it }
    }

    /** GET B/info + /api/bots + /api/settings：界面重新读一遍服务器上的当前配置 */
    suspend fun refresh() {
        guarded {
            loadInfo()
            server.bots = client.getArray("/api/bots").objects().map(BotProfile::from)
            server.profile(ref)?.let { if (it != profile) profile = it }
            loadSettings()
        }
    }

    suspend fun send(text: String, files: List<Attachment>) = guarded {
        client.post(
            "$basePath/messages",
            JSONObject().put("text", text).put("files", JSONArray(files.map { it.json() })),
        )
    }

    suspend fun stopAll(): JSONObject = guarded { client.post("$basePath/stop") }
    suspend fun stopReply(): JSONObject = guarded { client.post("$basePath/stop", query = mapOf("scope" to "reply")) }
    suspend fun regenerate(): JSONObject = guarded { client.post("$basePath/regenerate") }
    suspend fun edit(id: String, text: String): JSONObject = guarded {
        client.post("$basePath/messages/$id/edit", JSONObject().put("text", text))
    }
    suspend fun react(id: String, emoji: String): JSONObject = guarded {
        client.post("$basePath/messages/$id/react", JSONObject().put("emoji", emoji))
    }

    /** 任务的 pause|resume|cancel|retry|top|up；retry 返回的新任务 ID 在 message 里。 */
    suspend fun taskAction(id: String, action: String): String = guarded {
        client.post("$basePath/tasks/$id/$action").str("message")
    }

    suspend fun approve(id: String, decision: String): JSONObject = guarded {
        client.post("$basePath/approvals/$id", JSONObject().put("decision", decision))
    }

    suspend fun answerCard(id: String, answer: String? = null, dismiss: Boolean = false): JSONObject = guarded {
        val body = if (dismiss) JSONObject().put("dismiss", true) else JSONObject().put("answer", answer ?: "")
        client.post("$basePath/cards/$id", body)
    }

    /** 保存密钥：带 requestId 是回应密钥卡片；带 name 是直接新建（可选 description）。 */
    suspend fun saveSecret(value: String, requestId: String? = null, name: String? = null, description: String? = null): JSONObject = guarded {
        val body = JSONObject().put("value", value)
        if (!requestId.isNullOrEmpty()) body.put("request_id", requestId)
        if (!name.isNullOrEmpty()) body.put("name", name)
        if (description != null) body.put("description", description)
        client.post("$basePath/secrets", body)
    }

    suspend fun cancelSecretRequest(id: String): JSONObject = guarded {
        client.post("$basePath/secrets/requests/$id/cancel")
    }

    suspend fun runCommand(line: String): JSONObject = guarded {
        client.post("$basePath/commands", JSONObject().put("line", line))
    }

    suspend fun loadCommands(): List<CommandInfo> = guarded {
        client.getObject("/api/commands").arr("commands").objects().map(CommandInfo::from)
    }

    suspend fun loadSkills(): SkillsIndex = guarded { SkillsIndex.from(client.getObject("$basePath/skills")) }

    suspend fun loadExecutors(): ExecutorsConfig = guarded { ExecutorsConfig.from(client.getObject("$basePath/executors")) }

    /** 整体替换执行器白名单；校验失败后端返回 400 且旧值不变。 */
    suspend fun saveExecutors(config: ExecutorsConfig): ExecutorsConfig = guarded {
        ExecutorsConfig.from(client.put("$basePath/executors", config.payload()))
    }

    /** scope = instance（所有 Bot 共用）或 bot（只影响这个 Bot）；返回 ok 与 warnings。 */
    suspend fun saveEnv(scope: String, vars: Map<String, String>): JSONObject = guarded {
        val path = if (scope == "instance") "/api/env" else "$basePath/env"
        client.put(path, envBody(vars))
    }

    suspend fun loadEnv(scope: String): Map<String, String> = guarded {
        val path = if (scope == "instance") "/api/env" else "$basePath/env"
        val vars = client.getObject(path).obj("vars") ?: JSONObject()
        vars.keys().asSequence().associateWith { vars.str(it) }
    }

    suspend fun loadSettings(): ApiSettings = guarded {
        ApiSettings.from(client.getObject("$basePath/settings")).also { settings = it }
    }

    /** 渠道池是整个 reeve 共用的；这里读实例级入口，usage 标明各 Bot 的选择。 */
    suspend fun loadChannels(): ChannelsView = guarded { ChannelsView.from(client.getObject("/api/channels")) }

    /** 整体替换渠道池（实例级）。密钥用 api_key 写、省略表示沿用旧值。 */
    suspend fun saveChannels(channels: List<ApiChannel>): ChannelsView = guarded {
        val body = JSONObject().put("providers", JSONArray(channels.map { it.json() }))
        ChannelsView.from(client.post("/api/channels", body))
    }

    /** 只保存这个 Bot 用哪个渠道的哪个模型（渠道本身整个 reeve 共用） */
    suspend fun saveRoutes(chatChannel: String, chatModel: String, agentChannel: String, agentModel: String): ApiSettings = guarded {
        val body = JSONObject().put("default_provider_id", chatChannel).put("model", chatModel)
            .put("agent_provider_id", agentChannel).put("agent_model", agentModel)
        val saved = ApiSettings.from(client.post("$basePath/settings/routes", body))
        settings = saved
        loadInfo()
        saved
    }

    suspend fun setChatModel(channel: String, model: String) {
        val s = loadSettings()
        saveRoutes(channel, model, s.agentChannel, s.agentModel)
    }

    /** probe=false 只读缓存与安装状态（不起子进程）；打开设置页用 false，点「重新检测」用 true。 */
    suspend fun loadConnections(probe: Boolean = true): AgentConnections = guarded {
        AgentConnections.from(client.getObject("$basePath/settings/connections", mapOf("probe" to if (probe) "1" else "0")))
    }

    suspend fun fetchModels(channel: ApiChannel): List<String> = guarded {
        val body = JSONObject().put("provider_id", channel.id).put("base_url", channel.baseUrl)
        if (!channel.authRequired) body.put("api_key", "") else channel.apiKey?.let { body.put("api_key", it) }
        client.post("/api/settings/models", body).arr("models").strings()
    }

    suspend fun testChannel(channel: ApiChannel, model: String): Int = guarded {
        val body = JSONObject().put("provider_id", channel.id).put("base_url", channel.baseUrl).put("model", model)
        if (!channel.authRequired) body.put("api_key", "") else channel.apiKey?.let { body.put("api_key", it) }
        client.post("/api/settings/test", body).optInt("latency_ms", 0)
    }

    suspend fun schedules(): List<Schedule> = guarded {
        client.getArray("$basePath/schedules").objects().map(Schedule::from)
    }

    suspend fun schedule(id: String, action: String): JSONObject = guarded { client.post("$basePath/schedules/$id/$action") }

    suspend fun fileBytes(path: String): ByteArray = guarded {
        client.bytes("$basePath/files", mapOf("path" to path, "download" to "1"))
    }

    fun attach(name: String, mime: String, data: ByteArray) {
        val p = PendingUpload(name = name, size = data.size.toLong())
        uploads.add(p)
        scope.launch {
            val result = runCatching { client.upload("$basePath/uploads", name, mime, data) }
            val i = uploads.indexOfFirst { it.id == p.id }
            if (i >= 0) {
                uploads[i] = result.fold({ uploads[i].copy(attachment = it) }, { uploads[i].copy(error = ServerClient.describe(it)) })
            }
        }
    }

    companion object {
        /**
         * 解析一帧 SSE 的原始行。注释行（`: ping`）与空 data 返回 null；
         * seq 优先取 data 里的值，缺失时退回 `id:`；seq=0 的临时事件不推进续传位点。
         */
        fun parseFrame(lines: List<String>): SseFrame? {
            var id: Long? = null
            val payload = StringBuilder()
            for (line in lines) {
                if (line.isEmpty() || line.startsWith(":")) continue
                val field = line.substringBefore(':', line)
                val value = if (line.contains(':')) line.substringAfter(':').removePrefix(" ") else ""
                when (field) {
                    "id" -> id = value.trim().toLongOrNull()
                    "data" -> {
                        if (payload.isNotEmpty()) payload.append('\n')
                        payload.append(value)
                    }
                }
            }
            val text = payload.toString().trim()
            if (text.isEmpty()) return null
            val obj = runCatching { JSONTokener(text).nextValue() }.getOrNull() as? JSONObject ?: return null
            val kind = obj.str("kind")
            if (kind.isEmpty()) return null
            val seq = obj.optLong("seq", 0).let { if (it > 0) it else id ?: 0L }
            return SseFrame(seq, kind, obj.str("time"), obj.opt("data") ?: JSONObject())
        }

        /** 重连退避：2s、4s、8s… 封顶 30s。 */
        fun retryDelayMs(attempt: Int): Long = minOf(2_000L shl attempt.coerceIn(0, 4), 30_000L)
    }
}
