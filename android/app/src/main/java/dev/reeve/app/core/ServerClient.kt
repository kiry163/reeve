package dev.reeve.app.core

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.MultipartBody
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException
import java.net.ConnectException
import java.net.SocketTimeoutException
import java.net.URI
import java.net.URLDecoder
import java.net.UnknownHostException
import java.util.UUID
import java.util.concurrent.TimeUnit
import javax.net.ssl.SSLException

/** 一台 reeve 服务器的连接设置（保存在手机上）。token 是登录换来的设备会话令牌，不是密码。 */
data class ServerConfig(val id: String, val name: String, val url: String, val token: String) {
    val baseUrl: HttpUrl? get() = url.toHttpUrlOrNull()

    /** 会话令牌不进日志 / Crashlytics：toString 只显示是否有值 */
    override fun toString(): String =
        "ServerConfig(id=$id, name=$name, url=$url, token=${if (token.isEmpty()) "<空>" else "<已填>"})"

    /** 明文 HTTP 且不是本机地址：密码（登录时）和会话令牌在网络上不加密 */
    val insecure: Boolean get() = baseUrl?.let { it.scheme == "http" && !isLoopback(it.host) } ?: false

    /**
     * 通知深链里的 host[:port] 是否指向这台服务器。
     * 带端口时精确比较；不带端口时接受 HTTP/HTTPS 默认端口（80/443）。
     */
    fun matchesHost(linkHost: String): Boolean {
        val u = baseUrl ?: return false
        val (host, port) = splitHostPort(linkHost.trim())
        if (!u.host.equals(host, ignoreCase = true)) return false
        return if (port != null) u.port == port else u.port == 80 || u.port == 443
    }

    fun json(): JSONObject = JSONObject().put("id", id).put("name", name).put("url", url).put("token", token)

    companion object {
        fun from(j: JSONObject) = ServerConfig(j.str("id"), j.str("name"), j.str("url"), j.str("token"))

        fun isLoopback(host: String): Boolean {
            val h = host.lowercase()
            return h == "localhost" || h.endsWith(".localhost") || h.startsWith("127.") || h == "::1"
        }

        /**
         * 补全协议并去掉末尾 `/`：IP、IP:port、localhost 走 http，其它主机名走 https，
         * 已写的协议原样保留。空或无法解析时抛 IllegalArgumentException。
         */
        fun normalize(raw: String): String {
            val s = raw.trim().trimEnd('/')
            require(s.isNotEmpty()) { "地址不能为空" }
            val withScheme = if (s.contains("://")) s else (if (isPlainAuthority(s)) "http://" else "https://") + s
            val u = withScheme.toHttpUrlOrNull() ?: throw IllegalArgumentException("地址格式不对：$raw")
            return u.toString().trimEnd('/')
        }

        /** 解析配对串 `reeve://pair?v=1&host=<addr>`；只带地址，密码由用户在 App 里输入 */
        fun fromPairUri(uri: String): ServerConfig? {
            val u = runCatching { URI(uri.trim()) }.getOrNull() ?: return null
            if (!"reeve".equals(u.scheme, true) || !"pair".equals(u.host, true)) return null
            val q = query(u.rawQuery)
            if (q["v"] != "1") return null
            val host = q["host"]?.trim()?.takeIf { it.isNotEmpty() } ?: return null
            val url = runCatching { normalize(host) }.getOrNull() ?: return null
            val name = url.toHttpUrlOrNull()?.host ?: host.substringBefore(':')
            return ServerConfig(UUID.randomUUID().toString(), name, url, "")
        }

        /** 没写协议时是否该按 http 处理：IP 字面量或本机地址 */
        private fun isPlainAuthority(raw: String): Boolean {
            val authority = raw.substringBefore('/')
            val host = if (authority.startsWith("[")) authority.substringAfter('[').substringBefore(']')
            else authority.substringBefore(':')
            val ip = host.isNotEmpty() && host.all { it.isDigit() || it == '.' } && host.contains('.')
            return ip || isLoopback(host)
        }

        /** 解析 host[:port]；IPv6 用 [..] 包裹。没有端口时返回 null。 */
        private fun splitHostPort(raw: String): Pair<String, Int?> {
            if (raw.startsWith("[")) {
                val end = raw.indexOf(']')
                if (end < 0) return raw to null
                val host = raw.substring(1, end)
                val rest = raw.substring(end + 1)
                return host to rest.removePrefix(":").takeIf { it.isNotEmpty() }?.toIntOrNull()
            }
            val idx = raw.lastIndexOf(':')
            if (idx > 0) {
                val port = raw.substring(idx + 1).toIntOrNull()
                if (port != null) return raw.substring(0, idx) to port
            }
            return raw to null
        }

        // 供配对串和 reeve://open 深链共用；保留字面 `+`，只解码百分号编码。
        internal fun query(raw: String?): Map<String, String> {
            if (raw.isNullOrEmpty()) return emptyMap()
            return raw.split('&').mapNotNull { part ->
                if (part.isEmpty()) return@mapNotNull null
                val k = decode(part.substringBefore('='))
                if (k.isEmpty()) null else k to decode(part.substringAfter('=', ""))
            }.toMap()
        }

        internal fun decode(s: String): String =
            runCatching { URLDecoder.decode(s.replace("+", "%2B"), "UTF-8") }.getOrDefault(s)
    }
}

/** 后端返回的错误；status=401 时用子类 NeedsLogin。 */
open class ApiException(message: String, val status: Int = 0) : Exception(message)

/** 设备会话缺失、无效或已过期：需要在 App 里重新输入密码 */
class NeedsLogin(message: String = "登录已过期，请重新输入密码") : ApiException(message, 401)

/** GET /api/health */
data class Health(val ok: Boolean, val version: String, val apiVersion: Int) {
    /** 真的是一台 reeve 服务器：`ok` 为 true 且带正整数 `api_version`（别的服务可能也回 `{"ok":true}`）。 */
    val looksLikeReeve: Boolean get() = ok && apiVersion > 0

    companion object {
        fun from(j: JSONObject) = Health(j.bool("ok"), j.str("version"), j.optInt("api_version", 0))
    }
}

/** GET /api/auth/me */
data class AuthMe(val ok: Boolean, val enabled: Boolean, val authenticated: Boolean, val mode: String) {
    companion object {
        fun from(j: JSONObject) = AuthMe(j.bool("ok"), j.bool("enabled"), j.bool("authenticated"), j.str("mode"))
    }
}

/** GET /api/auth/login 的响应：设备会话令牌与过期时间 */
data class LoginSession(val token: String, val expiresAt: String?) {
    companion object {
        fun from(j: JSONObject) = LoginSession(j.str("token"), j.strOrNull("expires_at"))
    }
}

/** 和一台服务器说话：普通请求、上传、事件流。鉴权用登录换来的设备会话令牌（Bearer）。 */
class ServerClient(config: ServerConfig) {
    @Volatile var config: ServerConfig = config

    companion object {
        private val JSON_TYPE = "application/json; charset=utf-8".toMediaType()

        val http: OkHttpClient = OkHttpClient.Builder()
            .connectTimeout(15, TimeUnit.SECONDS)
            .readTimeout(30, TimeUnit.SECONDS)
            .followRedirects(false)
            .build()

        /** 事件流：服务器每 20 秒心跳，90 秒读不到东西就当断线重连 */
        val stream: OkHttpClient = http.newBuilder().readTimeout(90, TimeUnit.SECONDS).build()

        fun describe(e: Throwable): String = when (e) {
            is NeedsLogin -> e.message ?: "登录已过期"
            is ApiException -> e.message ?: "请求失败"
            is UnknownHostException -> "找不到这个地址，请检查域名或 IP"
            is ConnectException -> "连不上服务器：端口没开或服务没启动，或者手机和服务器不在一个网络"
            is SocketTimeoutException -> "连接超时：请检查网络、防火墙或安全组"
            is SSLException -> "HTTPS 证书有问题，无法建立安全连接"
            is IOException -> "网络错误：${e.message ?: "连接断开"}"
            else -> e.message ?: e.toString()
        }
    }

    fun url(path: String, query: Map<String, String> = emptyMap()): HttpUrl {
        val base = config.baseUrl ?: throw ApiException("地址格式不对")
        val b = base.newBuilder()
        path.trim('/').split('/').filter { it.isNotEmpty() }.forEach { b.addPathSegment(it) }
        query.forEach { (k, v) -> b.addQueryParameter(k, v) }
        return b.build()
    }

    fun request(path: String, query: Map<String, String> = emptyMap()): Request.Builder {
        val r = Request.Builder().url(url(path, query)).header("Accept", "application/json")
        config.token.takeIf { it.isNotEmpty() }?.let { r.header("Authorization", "Bearer $it") }
        return r
    }

    private fun check(resp: Response): String {
        val body = resp.body?.string() ?: ""
        if (resp.code == 401) throw NeedsLogin(message(body, 401))
        if (resp.code !in 200..299) throw ApiException(message(body, resp.code), resp.code)
        return body
    }

    private fun message(body: String, status: Int): String {
        runCatching { JSONObject(body).str("error") }.getOrNull()?.takeIf { it.isNotEmpty() }?.let { return it }
        val t = body.trim()
        if (t.isNotEmpty() && !t.startsWith("<")) return t.take(200)
        return when (status) {
            403 -> "服务器拒绝了请求（403）"
            404 -> "找不到这个接口（404），请确认地址指向 reeve 并且版本够新"
            429 -> "尝试太频繁，请稍后再试"
            502, 503, 504 -> "服务器暂时不可用（$status）"
            else -> "请求失败（$status）"
        }
    }

    private suspend fun exec(r: Request): String = withContext(Dispatchers.IO) {
        try {
            http.newCall(r).execute().use { check(it) }
        } catch (e: ApiException) {
            throw e
        } catch (e: Exception) {
            throw ApiException(describe(e))
        }
    }

    private fun errorIn(body: String) {
        val err = runCatching { JSONObject(body).str("error") }.getOrNull()
        if (!err.isNullOrEmpty()) throw ApiException(err, 400)
    }

    suspend fun getText(path: String, query: Map<String, String> = emptyMap()): String = exec(request(path, query).get().build())
    suspend fun getObject(path: String, query: Map<String, String> = emptyMap()): JSONObject = JSONObject(getText(path, query).ifBlank { "{}" })
    suspend fun getArray(path: String, query: Map<String, String> = emptyMap()): JSONArray = JSONArray(getText(path, query).ifBlank { "[]" })

    suspend fun post(
        path: String, body: JSONObject = JSONObject(), method: String = "POST", query: Map<String, String> = emptyMap(),
    ): JSONObject {
        val text = exec(request(path, query).method(method, body.toString().toRequestBody(JSON_TYPE)).build())
        errorIn(text)
        return runCatching { JSONObject(text) }.getOrElse { JSONObject() }
    }

    suspend fun put(path: String, body: JSONObject, query: Map<String, String> = emptyMap()): JSONObject =
        post(path, body, method = "PUT", query = query)

    suspend fun patch(path: String, body: JSONObject = JSONObject()): JSONObject = post(path, body, method = "PATCH")

    suspend fun delete(path: String, query: Map<String, String> = emptyMap()): JSONObject =
        post(path, JSONObject(), method = "DELETE", query = query)

    suspend fun bytes(path: String, query: Map<String, String>): ByteArray = withContext(Dispatchers.IO) {
        try {
            http.newCall(request(path, query).header("Accept", "*/*").get().build()).execute().use { resp ->
                if (resp.code == 401) throw NeedsLogin()
                if (resp.code !in 200..299) throw ApiException("下载失败（${resp.code}）", resp.code)
                resp.body?.bytes() ?: ByteArray(0)
            }
        } catch (e: ApiException) {
            throw e
        } catch (e: Exception) {
            throw ApiException(describe(e))
        }
    }

    suspend fun health(): Health = Health.from(getObject("/api/health"))

    /**
     * 用服务器密码换设备会话。401 表示密码不对（不是「会话过期」），
     * 所以这里不走 [check]，避免被映射成 [NeedsLogin]。
     */
    suspend fun login(password: String): LoginSession = withContext(Dispatchers.IO) {
        val req = Request.Builder().url(url("/api/auth/login"))
            .header("Accept", "application/json")
            .post(JSONObject().put("password", password).toString().toRequestBody(JSON_TYPE))
            .build()
        val text = try {
            http.newCall(req).execute().use { resp ->
                val body = resp.body?.string() ?: ""
                if (resp.code !in 200..299) throw ApiException(message(body, resp.code), resp.code)
                body
            }
        } catch (e: ApiException) {
            throw e
        } catch (e: Exception) {
            throw ApiException(describe(e))
        }
        LoginSession.from(JSONObject(text))
    }

    suspend fun me(): AuthMe = AuthMe.from(getObject("/api/auth/me"))

    suspend fun upload(path: String, name: String, mime: String, data: ByteArray): Attachment = withContext(Dispatchers.IO) {
        val form = MultipartBody.Builder().setType(MultipartBody.FORM)
            .addFormDataPart("file", name, data.toRequestBody(mime.toMediaType()))
            .build()
        val r = request(path).post(form).build()
        val text = try {
            http.newBuilder().writeTimeout(5, TimeUnit.MINUTES).build().newCall(r).execute().use { check(it) }
        } catch (e: ApiException) {
            throw e
        } catch (e: Exception) {
            throw ApiException(describe(e))
        }
        errorIn(text)
        val first = runCatching { JSONArray(text).optJSONObject(0) }.getOrNull() ?: throw ApiException("上传失败")
        Attachment.from(first)
    }
}
