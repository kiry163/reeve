package dev.reeve.app.core

import android.content.Context
import android.content.SharedPreferences
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import org.json.JSONArray
import org.json.JSONObject
import java.util.UUID

/** 服务器不认这枚 token 时统一给用户看的说明（401 / authenticated=false） */
private const val TOKEN_REJECTED = "token 不对：服务器不认这枚 token，可能已被轮换或抄漏了"

sealed interface ServerStatus {
    data object Connecting : ServerStatus
    data object Online : ServerStatus
    data object NeedsPairing : ServerStatus
    data class Offline(val error: String) : ServerStatus
}

/** App 里的一台服务器：连接设置、在线状态、上面的 Bot 列表 */
class ServerEntry(config: ServerConfig) {
    val id: String = config.id
    var config by mutableStateOf(config)
    var client = ServerClient(config)
    var status by mutableStateOf<ServerStatus>(ServerStatus.Connecting)
    var bots by mutableStateOf<List<BotProfile>>(emptyList())
    var onNeedsPairing: ((ServerEntry) -> Unit)? = null
    var poll: Job? = null

    val name: String get() = config.name
    val online: Boolean get() = status == ServerStatus.Online

    /** token 失效，需要用户重新配对（换 token） */
    val needsPairing: Boolean get() = status == ServerStatus.NeedsPairing

    fun markNeedsPairing() {
        if (needsPairing) return
        status = ServerStatus.NeedsPairing
        onNeedsPairing?.invoke(this)
    }

    fun profile(ref: BotRef): BotProfile? =
        if (ref.server == id) bots.firstOrNull { it.id == ref.bot } else null
}

/** 偏好设置 */
class Prefs(private val sp: SharedPreferences) {
    var keepAlive: Boolean
        get() = sp.getBoolean("keepAlive", false)
        set(v) = sp.edit().putBoolean("keepAlive", v).apply()
    var appearance: String
        get() = sp.getString("appearance", "system") ?: "system"
        set(v) = sp.edit().putString("appearance", v).apply()
    var notifyApproval: Boolean
        get() = sp.getBoolean("notifyApproval", true)
        set(v) = sp.edit().putBoolean("notifyApproval", v).apply()
    var notifyTask: Boolean
        get() = sp.getBoolean("notifyTask", true)
        set(v) = sp.edit().putBoolean("notifyTask", v).apply()
    var notifyReply: Boolean
        get() = sp.getBoolean("notifyReply", true)
        set(v) = sp.edit().putBoolean("notifyReply", v).apply()
}

/** 整个 App 的状态：服务器、会话、未读、正在看的 Bot */
class AppState(private val context: Context) {
    val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private val sp: SharedPreferences = context.getSharedPreferences("reeve", Context.MODE_PRIVATE)
    val prefs = Prefs(sp)

    val servers = mutableStateListOf<ServerEntry>()
    val sessions = mutableStateMapOf<String, BotSession>()
    val unread = mutableStateMapOf<String, Int>()

    /** 正在看的 Bot（在聊天页里）；不在聊天页时为 null */
    var viewing by mutableStateOf<BotRef?>(null)

    /** 深链 `reeve://pair?…` 带来的配对草稿及来源；配对页消费后清空 */
    var pendingPair by mutableStateOf<PendingPair?>(null)

    /**
     * 通知点击要打开的聊天（`RouteActivity` 写入，`MainActivity` 消费）。
     * 只在进程内传递，外部 Intent 无法伪造，因此 MainActivity 不需要从 intent 里读聊天引用。
     */
    var pendingRef by mutableStateOf<BotRef?>(null)

    /** 取走并清空待打开的聊天：进程被杀后由通知重建也能打开对应聊天。 */
    fun takePendingRef(): BotRef? = pendingRef.also { pendingRef = null }

    var foreground = false
    var appearance by mutableStateOf(prefs.appearance)
    var keepAlive by mutableStateOf(prefs.keepAlive)

    var onNotify: ((BotSession, String, NewsKind) -> Unit)? = null

    init {
        val list = runCatching { JSONArray(sp.getString("servers", "[]")) }.getOrElse { JSONArray() }
        for (j in list.objects()) servers.add(ServerEntry(ServerConfig.from(j)))
        servers.forEach(::wire)
    }

    val totalUnread: Int get() = unread.values.sum()

    fun server(id: String) = servers.firstOrNull { it.id == id }
    fun session(ref: BotRef) = sessions[ref.key]

    /** 这个 Bot 是否还在服务器的列表里（区分「正在连接」和「服务器上没有它了」）。 */
    fun knownBot(ref: BotRef): Boolean = server(ref.server)?.bots?.any { it.id == ref.bot } == true

    val allRefs: List<BotRef>
        get() = servers.flatMap { s -> s.bots.map { BotRef(s.id, it.id) } }

    private fun persist() {
        sp.edit().putString("servers", JSONArray(servers.map { it.config.json() }).toString()).apply()
    }

    private fun wire(s: ServerEntry) {
        s.onNeedsPairing = { entry -> stopSessions(entry) }
        startPolling(s)
    }

    /** 每 10 秒刷新 Bot 列表；每台服务器单独轮询 */
    private fun startPolling(s: ServerEntry) {
        s.poll?.cancel()
        s.poll = scope.launch {
            while (isActive) {
                refresh(s)
                delay(10_000)
            }
        }
    }

    suspend fun refresh(s: ServerEntry) {
        if (s.needsPairing) return
        try {
            s.bots = loadBots(s.client)
            s.status = ServerStatus.Online
            syncSessions(s)
        } catch (e: NeedsPairing) {
            s.markNeedsPairing()
        } catch (e: ApiException) {
            s.status = ServerStatus.Offline(e.message ?: "离线")
        } catch (e: Exception) {
            s.status = ServerStatus.Offline(ServerClient.describe(e))
        }
    }

    private suspend fun loadBots(client: ServerClient): List<BotProfile> =
        client.getArray("/api/bots").objects().map(BotProfile::from)

    private fun syncSessions(s: ServerEntry) {
        val alive = HashSet<String>()
        for (b in s.bots) {
            val ref = BotRef(s.id, b.id)
            alive.add(ref.key)
            val existing = sessions[ref.key]
            if (existing != null) {
                if (existing.profile != b) existing.profile = b
                existing.start()
            } else if (sessions.size < 48 || ref == viewing) {
                val session = BotSession(ref, b, s, scope)
                session.onNews = { sess, text, kind -> handleNews(sess, text, kind) }
                sessions[ref.key] = session
                session.start()
            }
        }
        val removed = sessions.keys.filter { key ->
            val ref = BotRef.parse(key) ?: return@filter false
            ref.server == s.id && key !in alive
        }
        for (key in removed) {
            sessions[key]?.stop()
            sessions.remove(key)
            unread.remove(key)
        }
    }

    fun stopSessions(s: ServerEntry) {
        sessions.values.filter { it.ref.server == s.id }.forEach { it.stop() }
    }

    /**
     * 配对页的保存：`GET /api/health` 可达且是 reeve（`ok=true`、`api_version` 非空）+
     * `GET /api/auth/me` 通过（`authenticated=true`）。失败抛 [ApiException]，
     * 文案区分「连不上」「不像 reeve 服务器」和「token 不对」。成功后走 [connect]；
     * 同一 host 已有条目时复用它（保留 id 与 Bot 偏好）。[editing] 非空时无论如何都改这一条：
     * id 强制为它，名称用草稿名称或原名称，避免命中另一台服务器时把别人的名字写进来。
     */
    suspend fun pair(draft: PairDraft, editing: ServerEntry? = null) {
        val config = runCatching {
            val others = servers.map { it.config }
            if (editing != null) draft.toEditConfig(editing.config, others) else draft.toConfig(others)
        }.getOrElse { throw ApiException(it.message ?: "地址格式不对") }
        val existing = editing ?: server(config.id)
        val client = ServerClient(config)
        val health = try {
            client.health()
        } catch (e: Exception) {
            throw ApiException("连不上 ${config.url}：${ServerClient.describe(e)}")
        }
        // 只是「有个 HTTP 服务在监听」不算配对成功：必须是 reeve（ok=true 且带 api_version）
        if (!health.looksLikeReeve) throw ApiException("这个地址不像 reeve 服务器")
        val me = try {
            client.me()
        } catch (e: NeedsPairing) {
            throw ApiException(TOKEN_REJECTED, 401)
        } catch (e: Exception) {
            throw ApiException("token 校验失败：${ServerClient.describe(e)}", (e as? ApiException)?.status ?: 0)
        }
        if (!me.authenticated) throw ApiException(TOKEN_REJECTED, 401)
        connect(existing, config.name, config.url, config.token)
    }

    /** 添加服务器，或改用新的 token 重新配对 */
    suspend fun connect(existing: ServerEntry?, name: String, address: String, token: String) {
        val url = runCatching { ServerConfig.normalize(address) }
            .getOrElse { throw ApiException("地址格式不对，例如 https://bot.example.com 或 http://1.2.3.4:18800") }
        val secret = token.trim()
        if (secret.isEmpty()) throw ApiException("请填写配对串里的 token")
        var config = ServerConfig(existing?.id ?: UUID.randomUUID().toString(), name.trim(), url, secret)
        val client = ServerClient(config)
        client.me()
        val bots = loadBots(client)
        if (config.name.isEmpty()) config = config.copy(name = bots.firstOrNull { it.id == "main" }?.name ?: config.baseUrl?.host ?: "reeve")
        client.config = config

        val s = existing ?: ServerEntry(config).also { servers.add(it) }
        if (existing != null) {
            stopSessions(existing)
            sessions.keys.filter { it.startsWith(existing.id + "|") }.forEach { sessions.remove(it); unread.remove(it) }
        }
        s.config = config
        s.client = client
        s.bots = bots
        s.status = ServerStatus.Online
        persist()
        if (existing == null) wire(s) else startPolling(s)
        syncSessions(s)
    }

    /** 换掉一台服务器的 token（重新配对）；下次轮询用新凭据重连 */
    fun replaceToken(id: String, token: String) {
        val s = server(id) ?: return
        val secret = token.trim()
        if (secret.isEmpty()) return
        s.config = s.config.copy(token = secret)
        s.client.config = s.config
        s.status = ServerStatus.Connecting
        persist()
        stopSessions(s)
        scope.launch { refresh(s) }
    }

    fun remove(s: ServerEntry) {
        s.poll?.cancel()
        stopSessions(s)
        sessions.keys.filter { it.startsWith(s.id + "|") }.forEach { sessions.remove(it); unread.remove(it) }
        servers.remove(s)
        persist()
    }

    suspend fun checkUpdate(s: ServerEntry): JSONObject = s.client.getObject("/api/update")

    suspend fun saveBot(s: ServerEntry, existing: BotProfile?, name: String, instructions: String): String? {
        val body = JSONObject().put("name", name).put("instructions", instructions)
        val id = if (existing != null) {
            s.client.patch("/api/bots/${existing.id}", body)
            existing.id
        } else {
            s.client.post("/api/bots", body).str("id").ifEmpty { null }
        }
        refresh(s)
        return id
    }

    suspend fun deleteBot(ref: BotRef, purge: Boolean) {
        val s = server(ref.server) ?: return
        s.client.delete("/api/bots/${ref.bot}", query = if (purge) mapOf("purge" to "1") else emptyMap())
        sessions[ref.key]?.stop()
        sessions.remove(ref.key)
        refresh(s)
    }

    fun changeAppearance(v: String) { prefs.appearance = v; appearance = v }
    fun changeKeepAlive(v: Boolean) { prefs.keepAlive = v; keepAlive = v }

    fun markRead(ref: BotRef) { unread.remove(ref.key) }

    private fun handleNews(session: BotSession, text: String, kind: NewsKind) {
        // 只在「前台且正停在这个 Bot 的聊天页」时算已读、不打扰；后台即使会话还停在聊天页也要提醒。
        val viewingThis = foreground && viewing == session.ref
        if (!viewingThis) unread[session.ref.key] = (unread[session.ref.key] ?: 0) + 1
        if (shouldNotify(kind, notifySwitches, foreground, viewingThis)) onNotify?.invoke(session, text, kind)
    }

    private val notifySwitches: NotifySwitches
        get() = NotifySwitches(prefs.notifyApproval, prefs.notifyTask, prefs.notifyReply)
}
