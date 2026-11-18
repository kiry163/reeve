package dev.reeve.app.core

import java.net.URI

/** 外部推送点击带来的聊天引用；只带 Bot 和可选的服务器 host，不带任何凭据。 */
data class OpenLink(val bot: String, val host: String?)

/**
 * 解析 `reeve://open?bot=…&host=…`。
 *
 * 这是导出给系统/ntfy App 的导航链接：只允许打开已经配置好的服务器，
 * 不自动配对、不读密码/token，也不把 host 当作服务器 id。
 */
fun parseOpenLink(raw: String?): OpenLink? {
    val u = runCatching { URI(raw?.trim() ?: return null) }.getOrNull() ?: return null
    if (!"reeve".equals(u.scheme, true) || !"open".equals(u.host, true)) return null
    val q = ServerConfig.query(u.rawQuery)
    val bot = q["bot"]?.trim().orEmpty()
    if (bot.isEmpty() || bot.length > 128) return null
    val host = q["host"]?.trim()?.takeIf { it.isNotEmpty() }
    if (host != null && (host.length > 255 || host.any { it.isWhitespace() || it.code < 0x20 || it.code == 0x7f })) {
        return null
    }
    return OpenLink(bot, host)
}

/**
 * 把深链落到已配置的服务器。有 host 时按 host[:port] 匹配；没有 host 时只有一台服务器才用。
 * 匹配不到返回 null：App 仍会打开，只是不跳到聊天。
 */
fun resolveOpenRef(link: OpenLink, servers: List<ServerConfig>): BotRef? {
    val host = link.host
    val server = if (host != null) {
        servers.firstOrNull { it.matchesHost(host) }
    } else {
        servers.singleOrNull()
    } ?: return null
    return BotRef(server.id, link.bot)
}
