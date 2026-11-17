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

/** 服务器不认这枚设备会话时统一给用户看的说明（401 / authenticated=false） */
private const val LOGIN_REJECTED = "登录已失效：请重新输入服务器密码"

sealed interface ServerStatus {
    data object Connecting : ServerStatus
    data object Online : ServerStatus
    data object NeedsLogin : ServerStatus
    data class Offline(val error: String) : ServerStatus
}

/** App 里的一台服务器：连接设置、在线状态、上面的 Bot 列表 */
class ServerEntry(config: ServerConfig) {
    val id: String = config.id
    var config by mutableStateOf(config)
    var client = ServerClient(config)
    var status by mutableStateOf<ServerStatus>(ServerStatus.Connecting)
    var bots by mutableStateOf<List<BotProfile>>(emptyList())
    var onNeedsLogin: ((ServerEntry) -> Unit)? = null
    var poll: Job? = null

    val name: String get() = config.name
    val online: Boolean get() = status == ServerStatus.Online

    /** 登录失效，需要用户重新输入密码 */
    val needsLogin: Boolean get() = status == ServerStatus.NeedsLogin

    fun markNeedsLogin() {
        if (needsLogin) return
        status = ServerStatus.NeedsLogin
        onNeedsLogin?.invoke(this)
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
        s.onNeedsLogin = { entry -> stopSessions(entry) }
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
        if (s.needsLogin) return
        try {
            s.bots = loadBots(s.client)
            s.status = ServerStatus.Online
            syncSessions(s)
        } catch (e: NeedsLogin) {
            s.markNeedsLogin()
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
     * 配对页的保存：`GET /api/health` 可达且是 reeve（`ok=true`、`api_version` 非空），
     * 然后用密码 `POST /api/auth/login` 换取设备会话，再 `GET /api/auth/me` 确认。失败抛 [ApiException]，
     * 文案区分「连不上」「不像 reeve 服务器」和「密码不对」。成功后走 [attach]；
     * 同一 host 已有条目时复用它（保留 id 与 Bot 偏好）。[editing] 非空时无论如何都改这一条：
     * id 强制为它，名称用草稿名称或原名称，避免命中另一台服务器时把别人的名字写进来。
     */
    suspend fun pair(draft: PairDraft, editing: ServerEntry? = null) {
        val target = runCatching {
            val others = servers.map { it.config }
            if (editing != null) draft.toEditTarget(editing.config, others) else draft.toTarget(others)
        }.getOrElse { throw ApiException(it.message ?: "地址格式不对") }
        val existing = editing ?: server(target.id)
        val probe = ServerClient(ServerConfig(target.id, target.name, target.url, ""))
        val health = try {
            probe.health()
        } catch (e: Exception) {
            throw ApiException("连不上 ${target.url}：${ServerClient.describe(e)}")
        }
        // 只是「有个 HTTP 服务在监听」不算成功：必须是 reeve（ok=true 且带 api_version）
        if (!health.looksLikeReeve) throw ApiException("这个地址不像 reeve 服务器")

        val password = draft.password.trim()
        if (password.isEmpty()) throw ApiException("请输入服务器密码")
        val token = try {
            probe.login(password).token
        } catch (e: ApiException) {
            throw ApiException("登录失败：${e.message}", e.status)
        }
        if (token.isEmpty()) throw ApiException("登录失败：服务器没有返回会话")

        val config = ServerConfig(target.id, target.name, target.url, token)
        val client = ServerClient(config)
        val me = try {
            client.me()
        } catch (e: NeedsLogin) {
            throw ApiException(LOGIN_REJECTED, 401)
        } catch (e: Exception) {
            throw ApiException("会话校验失败：${ServerClient.describe(e)}", (e as? ApiException)?.status ?: 0)
        }
        if (!me.authenticated) throw ApiException(LOGIN_REJECTED, 401)
        attach(existing, config, client)
    }

    /** 保存登录成功的服务器，并接上事件流。 */
    private suspend fun attach(existing: ServerEntry?, config: ServerConfig, client: ServerClient) {
        val bots = loadBots(client)
        var finalConfig = config
        if (finalConfig.name.isEmpty()) {
            finalConfig = finalConfig.copy(name = bots.firstOrNull { it.id == "main" }?.name ?: finalConfig.baseUrl?.host ?: "reeve")
        }
        client.config = finalConfig

        val s = existing ?: ServerEntry(finalConfig).also { servers.add(it) }
        if (existing != null) {
            stopSessions(existing)
            sessions.keys.filter { it.startsWith(existing.id + "|") }.forEach { sessions.remove(it); unread.remove(it) }
        }
        s.config = finalConfig
        s.client = client
        s.bots = bots
        s.status = ServerStatus.Online
        persist()
        if (existing == null) wire(s) else startPolling(s)
        syncSessions(s)
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
