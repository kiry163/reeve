package dev.reeve.app.core

/**
 * 通知的纯逻辑：渠道、摘要、抑制。Android 相关的部分（NotificationCompat、PendingIntent）
 * 在 [dev.reeve.app.ReeveApp] 里，这里不 import 任何 android.*，可以直接单测。
 */

/** `alerts`：等你确认的审批。 */
const val CH_ALERTS = "alerts"

/** `replies`：任务结束与助手的新回复。 */
const val CH_REPLIES = "replies"

/** `keepalive`：前台服务的常驻通知（MIN、无角标）。 */
const val CH_KEEPALIVE = "keepalive"

/** 通知正文最多这么多字符；不含 token，也不放工具参数原文。 */
const val SUMMARY_LIMIT = 120

/** 审批提醒的固定文案：不带工具名、参数和风险等级以外的任何原始内容。 */
const val APPROVAL_NOTICE = "有一个操作等你确认"

/** 三类提醒各自的开关（默认全开，持久化在 [Prefs]）。 */
data class NotifySwitches(
    val approval: Boolean = true,
    val task: Boolean = true,
    val reply: Boolean = true,
) {
    fun allows(kind: NewsKind): Boolean = when (kind) {
        NewsKind.Approval -> approval
        NewsKind.Task -> task
        NewsKind.Reply -> reply
    }
}

/** 审批走 `alerts`（HIGH），任务终态和助手回复都走 `replies`（DEFAULT）。 */
fun notifyChannel(kind: NewsKind): String = when (kind) {
    NewsKind.Approval -> CH_ALERTS
    NewsKind.Task -> CH_REPLIES
    NewsKind.Reply -> CH_REPLIES
}

/**
 * 通知去重标签：三类各自一条（同一类的后一条覆盖前一条），
 * 这样任务结束的提醒不会被随后的回复顶掉。
 */
fun noticeTag(kind: NewsKind): String = when (kind) {
    NewsKind.Approval -> "approval"
    NewsKind.Task -> "task"
    NewsKind.Reply -> "reply"
}

private val whitespace = Regex("\\s+")

/**
 * 折叠换行与连续空白；超过 [limit] 个字符时截断成 `…` 结尾。
 * 按 code point 截断（不切开 emoji 的代理对），末尾空白不会留在省略号前。
 */
fun notifySummary(text: String, limit: Int = SUMMARY_LIMIT): String {
    require(limit >= 1) { "limit 必须 ≥ 1" }
    val flat = text.trim().replace(whitespace, " ")
    if (flat.codePointCount(0, flat.length) <= limit) return flat
    var end = flat.offsetByCodePoints(0, limit - 1)
    while (end > 0 && flat[end - 1].isWhitespace()) end--
    return flat.substring(0, end) + "…"
}

/** 「任务「x」完成了」：标题先截断，保证整行不超过 [SUMMARY_LIMIT] 且后缀不被切掉。 */
fun taskNotice(title: String, verb: String, limit: Int = SUMMARY_LIMIT): String {
    val head = "任务「"
    val tail = "」$verb"
    val room = (limit - head.length - tail.length).coerceAtLeast(1)
    return head + notifySummary(title, room) + tail
}

/**
 * 该不该弹通知：三类开关各管自己；只有「App 在前台，而且正停在这个 Bot 的任意页面」
 * （聊天/渠道/环境变量/执行器/技能/Bot 编辑）时才不打扰。
 * 后台即使会话还停在那个页面（`viewing` 没清）也必须提醒。
 */
fun shouldNotify(kind: NewsKind, switches: NotifySwitches, foreground: Boolean, viewingThisBot: Boolean): Boolean =
    switches.allows(kind) && !(foreground && viewingThisBot)

/**
 * 通知标题：只有一台服务器时只写 Bot 名，多台时带上服务器名。
 * [publicTitle] 是锁屏公开版本的标题：**只有 Bot 名**，不含摘要、服务器名或任何消息内容。
 */
data class NoticeTexts(val title: String, val publicTitle: String)

fun noticeTexts(botName: String, serverName: String, serverCount: Int): NoticeTexts = NoticeTexts(
    title = if (serverCount == 1) botName else "$botName · $serverName",
    publicTitle = botName,
)
