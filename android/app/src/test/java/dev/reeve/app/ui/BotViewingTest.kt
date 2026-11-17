package dev.reeve.app.ui

import dev.reeve.app.core.BotRef
import dev.reeve.app.core.ServerStatus
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/** 抑制范围：该 Bot 的任意页面都算「正在看」；找不到会话时别在冷启动阶段报错。 */
class BotViewingTest {
    private val ref = BotRef("srv-1", "main")

    @Test
    fun everyPageOfThatBotCountsAsViewing() {
        val pages = listOf(
            Screen.Chat(ref),
            Screen.Channels(ref),
            Screen.Env(ref),
            Screen.Executors(ref),
            Screen.Skills(ref),
            Screen.BotEditor("srv-1", ref),
        )
        for (page in pages) assertEquals(page.toString(), ref, botOf(page))
    }

    @Test
    fun otherPagesDoNot() {
        val pages = listOf(
            Screen.Home, Screen.Scheduler, Screen.Settings,
            Screen.Server("srv-1"), Screen.AddServer(), Screen.Appearance, Screen.Notifications,
            // 新建 Bot：还没有 ref
            Screen.BotEditor("srv-1", null),
            // 未指定 Bot 的环境变量/执行器/技能页
            Screen.Env(), Screen.Executors(), Screen.Skills(),
        )
        for (page in pages) assertNull(page.toString(), botOf(page))
    }

    @Test
    fun coldStartSaysConnectingInsteadOfUnreachable() {
        assertEquals("正在连接…", missingChatText(ServerStatus.Connecting, botKnown = false))
        assertEquals("正在连接…", missingChatText(ServerStatus.Connecting, botKnown = true))
        // 服务器已在线、Bot 在列表里，只是 SSE 会话还没建起来
        assertEquals("正在连接…", missingChatText(ServerStatus.Online, botKnown = true))
        // 在线但列表里没这个 Bot：真的连不上
        assertEquals("这个 Bot 现在连不上", missingChatText(ServerStatus.Online, botKnown = false))
    }

    @Test
    fun missingChatTextReportsRealProblems() {
        assertEquals("这台服务器已移除", missingChatText(null, botKnown = false))
        assertEquals("token 已失效，需要重新配对", missingChatText(ServerStatus.NeedsPairing, botKnown = true))
        assertEquals(
            "这个 Bot 现在连不上：连接超时",
            missingChatText(ServerStatus.Offline("连接超时"), botKnown = true),
        )
    }
}
