package dev.reeve.app.core

import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import java.util.UUID

/**
 * 待配对的草稿 + 来源。
 *
 * 深链 `reeve://pair?…` 只带地址，可以由外部 App / 网页触发，所以 [fromExternalLink] 为 true 时，
 * 配对页必须展示完整地址并要求用户勾选「我已核对地址」。密码始终由用户自己输入，不进入深链或二维码。
 * 扫码、粘贴、手填都在 App 内部产生，来源不可被外部伪造，[fromExternalLink] 为 false。
 */
data class PendingPair(val draft: PairDraft, val fromExternalLink: Boolean) {
    companion object {
        /** 深链 → 待配对草稿（外部来源）；不是合法配对串时返回 null。 */
        fun fromLink(raw: String?): PendingPair? =
            PairDraft.fromPairString(raw)?.let { PendingPair(it, fromExternalLink = true) }

        /** 扫码 / 粘贴 / 手填 → 草稿（App 内部来源，不需要额外确认）。 */
        fun fromInside(draft: PairDraft): PendingPair = PendingPair(draft, fromExternalLink = false)
    }
}

/**
 * 「登录」按钮是否可用：地址与密码都非空，且外部链接来源必须由用户勾选「我已核对地址」。
 */
fun canPair(
    address: String,
    password: String,
    fromExternalLink: Boolean,
    addressConfirmed: Boolean,
    busy: Boolean = false,
): Boolean = !busy && address.isNotBlank() && password.isNotBlank() && (!fromExternalLink || addressConfirmed)

/** 登录成功后要保存的服务器地址信息（会话令牌由登录响应提供）。 */
data class PairTarget(val id: String, val name: String, val url: String)

/**
 * 配对页上的草稿：地址、密码、可选名称。
 *
 * 扫码、粘贴配对串、深链、手填最后都落到同一个草稿；密码只在登录请求里使用，不写入设置。
 * [toTarget] 是唯一把地址变成要保存的服务器身份的地方，因此 trim、协议补全、重复 host 复用 id
 * 都只有一份实现。
 */
data class PairDraft(
    val address: String = "",
    val password: String = "",
    val name: String = "",
) {
    /** 密码不进日志：toString 只显示是否有值 */
    override fun toString(): String =
        "PairDraft(address=$address, password=${if (password.isEmpty()) "<空>" else "<已填>"}, name=$name)"

    /**
     * 草稿转成服务器身份。
     *
     * - 地址按 [ServerConfig.normalize] 去空白、补协议、去末尾 `/`；
     * - 已经有同一 host:port 的服务器时复用它的 `id`（Bot 偏好、会话都挂在 id 上），
     *   名称只在新草稿填了才覆盖；不同 host 用 [newId] 新建。
     */
    fun toTarget(existing: List<ServerConfig>, newId: String = UUID.randomUUID().toString()): PairTarget {
        val url = ServerConfig.normalize(address)
        val same = existing.firstOrNull { sameEndpoint(it.url, url) }
        return PairTarget(
            id = same?.id ?: newId,
            name = name.trim().ifEmpty { same?.name ?: "" },
            url = url,
        )
    }

    /**
     * 编辑已有服务器时的身份：`id` 强制为 [target] 的 id，名称只认草稿里填的，否则沿用原名称。
     * 这样即使地址刚好命中列表里另一台服务器，也不会把别人的 id / 名称串过来。
     */
    fun toEditTarget(target: ServerConfig, existing: List<ServerConfig>): PairTarget =
        toTarget(existing).copy(id = target.id, name = name.trim().ifBlank { target.name })

    companion object {
        fun fromConfig(c: ServerConfig) = PairDraft(address = c.url, password = "", name = c.name)

        /** 配对串（扫码 / 粘贴 / 深链）→ 草稿；不是合法的 `reeve://pair?v=1&…` 时返回 null */
        fun fromPairString(raw: String?): PairDraft? {
            val c = ServerConfig.fromPairUri(raw ?: return null) ?: return null
            return PairDraft(address = c.url)
        }

        /**
         * 同一个 host 且端口相同就当作同一台服务器。
         *
         * 端口比较用「实际端口」：`http://host` 是 80、`https://host` 是 443，两者都是各自协议的
         * 默认端口，所以 `http://host` → `https://host` 视为同一台，就地换协议而不是新增。
         * 任一侧写了显式端口（如 `:18800`）时按端口字面比较：`http://host:18800` 与
         * `https://host` 是两台，`http://host:18800` 与 `https://host:18800` 仍是同一台。
         */
        private fun sameEndpoint(a: String, b: String): Boolean {
            val ua = a.toHttpUrlOrNull() ?: return false
            val ub = b.toHttpUrlOrNull() ?: return false
            if (!ua.host.equals(ub.host, ignoreCase = true)) return false
            if (ua.port == ub.port) return true
            return ua.port == defaultPort(ua.scheme) && ub.port == defaultPort(ub.scheme)
        }

        private fun defaultPort(scheme: String): Int = if (scheme.equals("https", ignoreCase = true)) 443 else 80
    }
}
