package dev.reeve.app

import dev.reeve.app.core.Fmt
import dev.reeve.app.core.TaskStatus
import org.junit.Assert.assertEquals
import org.junit.Test

class FmtTest {

    @Test
    fun stopReasonText() {
        assertEquals("等你补充", Fmt.pauseReason("user"))
        assertEquals("已中断，需要重新执行", Fmt.pauseReason("interrupted"))
        assertEquals("先暂停，等聊完再继续", Fmt.pauseReason("chat"))
        assertEquals("长时间没有进展", Fmt.pauseReason("stall"))
        assertEquals("待办", Fmt.pauseReason("held"))
        assertEquals("服务重启后暂停 · 进度已保存", Fmt.pauseReason("restart"))
        assertEquals("", Fmt.pauseReason(null))
        assertEquals("", Fmt.pauseReason("没见过的原因"))
    }

    @Test
    fun statusLineUsesStopReason() {
        val task = dev.reeve.app.core.BotTask(
            id = "t1", title = "x", input = "x", status = TaskStatus.paused,
            stopReason = "interrupted", created = 0.0, prio = 0.0, lastEvent = 0.0, lastBeat = 0.0,
        )
        assertEquals("已中断，需要重新执行", Fmt.statusLine(task, 0.0, null))
    }

    @Test
    fun statusLineMarksStalledCancel() {
        val task = dev.reeve.app.core.BotTask(
            id = "t1", title = "x", input = "x", status = TaskStatus.cancelled,
            stopReason = "stall", created = 0.0, prio = 0.0, lastEvent = 0.0, lastBeat = 0.0,
        )
        assertEquals("长时间没有进展，已自动停止 · 0 步", Fmt.statusLine(task, 0.0, null))
    }
}
