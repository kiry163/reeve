package dev.reeve.app

import dev.reeve.app.core.BotSession
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class SseFrameTest {

    @Test
    fun parsesIdAndData() {
        val f = BotSession.parseFrame(
            listOf("id: 42", """data: {"seq":42,"kind":"message","data":{}}"""),
        )
        assertEquals(42L, f!!.seq)
        assertEquals("message", f.kind)
    }

    @Test
    fun ignoresCommentsAndEmpty() {
        assertNull(BotSession.parseFrame(listOf(": ping")))
        assertNull(BotSession.parseFrame(listOf("data:")))
        assertNull(BotSession.parseFrame(listOf("event: message")))
    }

    @Test
    fun treatsSeqZeroAsVolatile() {
        val f = BotSession.parseFrame(listOf("""data: {"seq":0,"kind":"chat.delta","data":{"text":"x"}}"""))
        assertEquals(0L, f!!.seq)
        assertEquals("chat.delta", f.kind)
    }

    @Test
    fun fallsBackToIdWhenDataHasNoSeq() {
        val f = BotSession.parseFrame(listOf("id: 9", """data: {"kind":"message","data":{"id":"m1"}}"""))
        assertEquals(9L, f!!.seq)
    }

    @Test
    fun keepsArrayDataForHiddenEvents() {
        val f = BotSession.parseFrame(listOf("id: 3", """data: {"seq":3,"kind":"message.hidden","data":["m1","m2"]}"""))
        assertEquals(listOf("m1", "m2"), (f!!.data as org.json.JSONArray).strings())
    }

    @Test
    fun retryDelayDoublesAndCaps() {
        assertEquals(2_000L, BotSession.retryDelayMs(0))
        assertEquals(4_000L, BotSession.retryDelayMs(1))
        assertEquals(8_000L, BotSession.retryDelayMs(2))
        assertEquals(30_000L, BotSession.retryDelayMs(6))
        assertEquals(30_000L, BotSession.retryDelayMs(99))
    }
}

private fun org.json.JSONArray.strings(): List<String> = (0 until length()).map { optString(it) }
