package dev.reeve.app

import dev.reeve.app.core.BotState
import dev.reeve.app.core.EventReducer
import dev.reeve.app.core.Item
import dev.reeve.app.core.TaskStatus
import dev.reeve.app.core.WireEvent
import org.json.JSONTokener
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class EventReducerTest {

    private fun ev(seq: Long, kind: String, data: String): WireEvent =
        WireEvent(seq, kind, "", JSONTokener(data).nextValue())

    @Test
    fun replaysMessagesAndTasksInOrder() {
        var s = EventReducer.empty()
        s = EventReducer.apply(s, ev(1, "message", """{"id":"m1","role":"user","text":"hi","created_at":""}"""))
        s = EventReducer.apply(s, ev(2, "task.created", """{"id":"t1","title":"x","input":"x","status":"queued","created_at":""}"""))
        s = EventReducer.apply(s, ev(3, "agent", """{"task_id":"t1","type":"task.started","data":{}}"""))
        s = EventReducer.apply(s, ev(4, "agent", """{"task_id":"t1","type":"task.completed","data":{"status":"completed","output":"ok"}}"""))

        assertEquals(2, s.items.size)
        assertTrue(s.items[0] is Item.MessageItem && s.items[0].id == "m1")
        assertTrue(s.items[1] is Item.TaskItem && s.items[1].id == "t1")
        assertEquals(TaskStatus.completed, s.tasks["t1"]!!.status)
        assertEquals("ok", s.tasks["t1"]!!.output)
        assertEquals(4L, s.lastSeq)
    }

    @Test
    fun ignoresDuplicateAndOldSeq() {
        var s = EventReducer.empty()
        s = EventReducer.apply(s, ev(5, "message", """{"id":"m1","role":"user","text":"hi","created_at":""}"""))
        s = EventReducer.apply(s, ev(5, "message", """{"id":"m2","role":"user","text":"dup","created_at":""}"""))
        assertEquals(1, s.msgs.size)
        assertEquals(5L, s.lastSeq)

        s = EventReducer.apply(s, ev(3, "message", """{"id":"m3","role":"user","text":"old","created_at":""}"""))
        assertEquals(1, s.msgs.size)
        assertEquals(5L, s.lastSeq)
    }

    @Test
    fun terminalStatusFromAgentAndTaskUpdated() {
        var s = EventReducer.empty()
        s = EventReducer.apply(s, ev(1, "task.created", """{"id":"t1","title":"x","input":"x","status":"queued","created_at":""}"""))
        s = EventReducer.apply(s, ev(2, "agent", """{"task_id":"t1","type":"task.started","data":{}}"""))
        assertEquals(TaskStatus.running, s.tasks["t1"]!!.status)

        s = EventReducer.apply(s, ev(3, "agent", """{"task_id":"t1","type":"task.completed","data":{"status":"completed","output":"ok"}}"""))
        assertEquals(TaskStatus.completed, s.tasks["t1"]!!.status)
    }

    @Test
    fun queuedCancelFromTaskUpdatedEndsTheTask() {
        var s = EventReducer.empty()
        s = EventReducer.apply(s, ev(1, "task.created", """{"id":"t1","title":"排队中取消","input":"x","status":"queued","created_at":""}"""))
        // 后端「排队中取消」只写 task.updated，没有 agent 终态；必须采纳，否则任务永远停在 queued。
        s = EventReducer.apply(s, ev(2, "task.updated", """{"id":"t1","status":"cancelled","error":"在开始前被取消"}"""))
        assertEquals(TaskStatus.cancelled, s.tasks["t1"]!!.status)
        assertTrue(s.tasks["t1"]!!.ended != null)
        assertEquals("在开始前被取消", s.tasks["t1"]!!.error)
        assertFalse(s.tasks["t1"]!!.isOpen)

        // 启动失败同理
        s = EventReducer.apply(s, ev(3, "task.created", """{"id":"t2","title":"启动失败","input":"x","status":"queued","created_at":""}"""))
        s = EventReducer.apply(s, ev(4, "task.updated", """{"id":"t2","status":"failed","error":"no slots"}"""))
        assertEquals(TaskStatus.failed, s.tasks["t2"]!!.status)
        assertTrue(s.tasks["t2"]!!.ended != null)
    }

    @Test
    fun pausingClearedByRunningAndClearStopReason() {
        var s = EventReducer.empty()
        s = EventReducer.apply(s, ev(1, "task.created", """{"id":"t1","title":"x","input":"x","status":"running","created_at":""}"""))
        s = EventReducer.apply(s, ev(2, "task.pausing", """{"id":"t1","stop_reason":"user"}"""))
        assertTrue(s.tasks["t1"]!!.pausing)

        s = EventReducer.apply(s, ev(3, "task.updated", """{"id":"t1","status":"running","clear_stop_reason":true}"""))
        assertFalse(s.tasks["t1"]!!.pausing)
        assertNull(s.tasks["t1"]!!.stopReason)

        s = EventReducer.apply(s, ev(4, "task.pausing", """{"id":"t1","stop_reason":"user"}"""))
        s = EventReducer.apply(s, ev(5, "task.updated", """{"id":"t1","status":"queued"}"""))
        assertFalse(s.tasks["t1"]!!.pausing)
        assertNull(s.tasks["t1"]!!.stopReason)
    }

    @Test
    fun approvalCountTracksRequestedAndDecided() {
        var s = EventReducer.empty()
        s = EventReducer.apply(s, ev(1, "task.created", """{"id":"t1","title":"x","input":"x","status":"running","created_at":""}"""))
        s = EventReducer.apply(s, ev(2, "agent", """{"task_id":"t1","type":"approval.requested","data":{"id":"a1","tool_name":"shell","args":{},"risk":"exec","reason":"x"}}"""))
        assertEquals(1, s.tasks["t1"]!!.awaiting)
        assertEquals("pending", s.approvals["a1"]!!.status)
        assertTrue(s.items.any { it is Item.ApprovalItem && it.id == "a1" })

        // 重复的同一请求不重复计数、不重复插入时间线
        s = EventReducer.apply(s, ev(3, "agent", """{"task_id":"t1","type":"approval.requested","data":{"id":"a1","tool_name":"shell","args":{},"risk":"exec","reason":"x"}}"""))
        assertEquals(1, s.tasks["t1"]!!.awaiting)
        assertEquals(1, s.items.count { it is Item.ApprovalItem })

        s = EventReducer.apply(s, ev(4, "agent", """{"task_id":"t1","type":"approval.decided","data":{"approval_id":"a1","decision":"allow"}}"""))
        assertEquals(0, s.tasks["t1"]!!.awaiting)
        assertEquals("allow", s.approvals["a1"]!!.status)
    }

    @Test
    fun hiddenRemovesMessage() {
        var s = EventReducer.empty()
        s = EventReducer.apply(s, ev(1, "message", """{"id":"m1","role":"user","text":"hi"}"""))
        s = EventReducer.apply(s, ev(2, "message.hidden", """["m1"]"""))
        assertTrue(s.msgs.isEmpty())
        assertTrue(s.items.isEmpty())
    }

    @Test
    fun commandResultBecomesNoticeItem() {
        var s = EventReducer.empty()
        s = EventReducer.apply(s, ev(1, "command.result", """{"command":"/help","text":"…","ok":true}"""))
        assertEquals(1, s.items.count { it is Item.NoticeItem })
        assertEquals("notice", s.msgs["cmd_1"]!!.role)
        assertTrue(s.msgs["cmd_1"]!!.ok)

        s = EventReducer.apply(s, ev(2, "command.result", """{"command":"/bad","text":"失败","ok":false}"""))
        assertEquals(2, s.items.count { it is Item.NoticeItem })
        assertFalse(s.msgs["cmd_2"]!!.ok)
    }

    @Test
    fun taskUpdatedClearStopReasonWins() {
        var s = EventReducer.empty()
        s = EventReducer.apply(s, ev(1, "task.created", """{"id":"t1","title":"x","input":"x","status":"paused","stop_reason":"user","created_at":""}"""))
        s = EventReducer.apply(s, ev(2, "task.updated", """{"id":"t1","status":"running","clear_stop_reason":true}"""))
        assertNull(s.tasks["t1"]!!.stopReason)
        assertEquals(TaskStatus.running, s.tasks["t1"]!!.status)

        // 同时给了 stop_reason 时，clear_stop_reason 仍然优先
        s = EventReducer.apply(s, ev(3, "task.created", """{"id":"t2","title":"x","input":"x","status":"paused","stop_reason":"user","created_at":""}"""))
        s = EventReducer.apply(s, ev(4, "task.updated", """{"id":"t2","stop_reason":"user","clear_stop_reason":true}"""))
        assertNull(s.tasks["t2"]!!.stopReason)
    }

    @Test
    fun executorSurvivesReplay() {
        var s = EventReducer.empty()
        s = EventReducer.apply(s, ev(1, "task.created", """{"id":"t1","title":"x","input":"x","status":"queued","executor":"omp","created_at":""}"""))
        assertEquals("omp", s.tasks["t1"]!!.executor)
    }

    @Test
    fun volatileEventsDoNotAdvanceLastSeq() {
        var s = EventReducer.empty()
        s = EventReducer.apply(s, ev(7, "message", """{"id":"m1","role":"user","text":"hi"}"""))
        s = EventReducer.apply(s, ev(0, "chat.delta", """{"text":"你好"}"""))
        s = EventReducer.apply(s, ev(0, "chat.thinking", """{"text":"想"}"""))
        s = EventReducer.apply(s, ev(0, "chat.tool", """{"label":"读取了 a.txt"}"""))
        assertEquals(7L, s.lastSeq)
        assertEquals("你好", s.streaming)
        assertEquals("想", s.thinking)
        assertEquals("读取了 a.txt", s.toolLabel)
    }

    @Test
    fun busyStaysFalseUntilReplayDone() {
        var s = EventReducer.empty()
        s = EventReducer.apply(s, ev(0, "chat.status", """{"busy":true}"""))
        assertFalse(s.ready)
        assertFalse(s.busy)

        s = EventReducer.apply(s, ev(0, "replay.done", """{"busy":true}"""))
        assertTrue(s.ready)
        assertTrue(s.busy)

        s = EventReducer.apply(s, ev(0, "chat.status", """{"busy":false}"""))
        assertFalse(s.busy)
    }

    @Test
    fun messageAndCardEventsFeedMaps() {
        var s = EventReducer.empty()
        s = EventReducer.apply(s, ev(1, "message", """{"id":"m1","role":"assistant","text":"选一个","card":{"id":"c1","question":"谁?","options":["a","b"],"status":"pending"}}"""))
        assertEquals("pending", s.cards["c1"]!!.status)
        s = EventReducer.apply(s, ev(2, "card", """{"id":"c1","question":"谁?","options":["a","b"],"status":"answered","answer":"a"}"""))
        assertEquals("answered", s.cards["c1"]!!.status)
        assertEquals("a", s.msgs["m1"]!!.card!!.answer)
    }
}
