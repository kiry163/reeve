package dev.reeve.app

import dev.reeve.app.core.APPROVAL_NOTICE
import dev.reeve.app.core.CH_ALERTS
import dev.reeve.app.core.CH_REPLIES
import dev.reeve.app.core.NewsKind
import dev.reeve.app.core.NotifySwitches
import dev.reeve.app.core.SUMMARY_LIMIT
import dev.reeve.app.core.notifyChannel
import dev.reeve.app.core.notifySummary
import dev.reeve.app.core.noticeTag
import dev.reeve.app.core.noticeTexts
import dev.reeve.app.core.shouldNotify
import dev.reeve.app.core.taskNotice
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class NotifyTest {

    // ---------- 摘要截断 ----------

    @Test
    fun summaryKeepsShortText() {
        assertEquals(APPROVAL_NOTICE, notifySummary(APPROVAL_NOTICE))
        assertEquals(SUMMARY_LIMIT, notifySummary("字".repeat(SUMMARY_LIMIT)).length)
    }

    @Test
    fun summaryCollapsesWhitespace() {
        assertEquals("第一行 第二行 第三行", notifySummary("  第一行\n\n第二行\t第三行 \n"))
        assertEquals("", notifySummary(" \n\t "))
    }

    @Test
    fun summaryTruncatesLongTextTo120() {
        val text = notifySummary("字".repeat(500))
        assertEquals(SUMMARY_LIMIT, text.length)
        assertTrue(text.endsWith("…"))
        assertEquals("字".repeat(SUMMARY_LIMIT - 1) + "…", text)
    }

    @Test
    fun summaryDropsTrailingSpaceBeforeEllipsis() {
        // 截断点前正好是空白：空白不留在省略号前面。
        val text = notifySummary("a".repeat(118) + " b" + "c".repeat(3))
        assertEquals("a".repeat(118) + "…", text)
        assertEquals(119, text.length)
        assertTrue(text.endsWith("…"))
    }

    @Test
    fun summaryNeverSplitsSurrogatePairs() {
        // 截断点落在一对代理中间时按 code point 让位，emoji 不会被切成半个。
        val text = notifySummary("a".repeat(118) + "😀" + "b".repeat(10))
        assertEquals("a".repeat(118) + "😀…", text)
        assertEquals(SUMMARY_LIMIT, text.codePointCount(0, text.length))
        // 代理对完整：UTF-16 长度比 code point 数多 1
        assertEquals(SUMMARY_LIMIT + 1, text.length)
    }

    // ---------- 渠道选择 ----------

    @Test
    fun approvalGoesToAlertsChannel() {
        assertEquals(CH_ALERTS, notifyChannel(NewsKind.Approval))
        assertEquals("alerts", notifyChannel(NewsKind.Approval))
    }

    @Test
    fun taskTerminalGoesToRepliesChannel() {
        assertEquals(CH_REPLIES, notifyChannel(NewsKind.Task))
        assertEquals("replies", notifyChannel(NewsKind.Task))
    }

    @Test
    fun assistantReplyGoesToRepliesChannel() {
        assertEquals("replies", notifyChannel(NewsKind.Reply))
    }

    @Test
    fun eachKindGetsItsOwnNoticeTag() {
        // 三类各自一条：任务终态的提醒不会被随后的回复顶掉，也不能顶掉还没处理的审批。
        assertEquals("approval", noticeTag(NewsKind.Approval))
        assertEquals("task", noticeTag(NewsKind.Task))
        assertEquals("reply", noticeTag(NewsKind.Reply))
        assertEquals(3, NewsKind.entries.map(::noticeTag).toSet().size)
    }

    // ---------- 锁屏公开版本 ----------

    @Test
    fun publicVersionOnlyHasBotName() {
        val single = noticeTexts(botName = "reeve", serverName = "东京 VPS", serverCount = 1)
        assertEquals("reeve", single.title)
        assertEquals("reeve", single.publicTitle)

        val multi = noticeTexts(botName = "reeve", serverName = "东京 VPS", serverCount = 2)
        assertEquals("reeve · 东京 VPS", multi.title)
        // 公开版本只有 Bot 名：服务器名、摘要都不出现在锁屏上
        assertEquals("reeve", multi.publicTitle)
        assertFalse(multi.publicTitle.contains("东京"))
        assertFalse(multi.publicTitle.contains("任务", ignoreCase = true))
    }

    @Test
    fun publicVersionNeverCarriesSummary() {
        val texts = noticeTexts(botName = "reeve", serverName = "srv", serverCount = 1)
        val body = notifySummary("机密：把数据库密码发给我")
        assertTrue(body.isNotEmpty())
        assertEquals("reeve", texts.publicTitle)
        assertFalse(texts.publicTitle.contains(body))
        assertFalse(texts.publicTitle.contains("机密"))
    }

    // ---------- 任务文案 ----------

    @Test
    fun taskNoticeKeepsVerbAndFitsLimit() {
        assertEquals("任务「修好登录」完成了", taskNotice("修好登录", "完成了"))
        assertEquals("任务「修好登录」失败了", taskNotice("修好登录", "失败了"))

        val long = taskNotice("标题".repeat(200), "完成了")
        assertEquals(SUMMARY_LIMIT, long.length)
        assertTrue(long.startsWith("任务「"))
        assertTrue(long.endsWith("」完成了"))
        assertTrue(long.contains("…"))
    }

    // ---------- 抑制判定 ----------

    @Test
    fun suppressedOnlyWhenForegroundAndViewingThatBot() {
        val all = NotifySwitches()
        for (kind in NewsKind.entries) {
            assertFalse(kind.name, shouldNotify(kind, all, foreground = true, viewingThisBot = true))
            assertTrue(kind.name, shouldNotify(kind, all, foreground = true, viewingThisBot = false))
            assertTrue(kind.name, shouldNotify(kind, all, foreground = false, viewingThisBot = false))
            // 后台时 viewing 还停在聊天页（页面没清）：仍要提醒
            assertTrue(kind.name, shouldNotify(kind, all, foreground = false, viewingThisBot = true))
        }
    }

    @Test
    fun switchesGateEachKindIndependently() {
        val off = NotifySwitches(approval = false)
        assertFalse(shouldNotify(NewsKind.Approval, off, foreground = false, viewingThisBot = false))
        assertTrue(shouldNotify(NewsKind.Task, off, foreground = false, viewingThisBot = false))
        assertTrue(shouldNotify(NewsKind.Reply, off, foreground = false, viewingThisBot = false))

        val none = NotifySwitches(approval = false, task = false, reply = false)
        for (kind in NewsKind.entries) {
            assertFalse(kind.name, shouldNotify(kind, none, foreground = false, viewingThisBot = false))
        }
        assertTrue(NotifySwitches().allows(NewsKind.Approval))
    }
}
