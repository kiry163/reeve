package dev.reeve.app

import dev.reeve.app.core.CommandInfo
import dev.reeve.app.ui.commandQuery
import dev.reeve.app.ui.filterCommands
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class CommandFilterTest {

    private val commands = listOf(
        CommandInfo("env", listOf("e"), "查看和修改环境变量", "/env list"),
        CommandInfo("help", emptyList(), "列出所有命令", "/help"),
        CommandInfo("model", listOf("m"), "换聊天模型", "/model <名称>"),
        CommandInfo("memory", emptyList(), "查看记忆", "/memory"),
        CommandInfo("deploy", listOf("release"), "发布服务", "/deploy"),
    )

    @Test
    fun filtersByNameCaseInsensitive() {
        assertEquals(listOf("env"), filterCommands(commands, "ENV").map { it.name })
        assertEquals(listOf("help"), filterCommands(commands, "HELP").map { it.name })
    }

    @Test
    fun filtersByAlias() {
        assertEquals(listOf("deploy"), filterCommands(commands, "release").map { it.name })
    }

    @Test
    fun filtersBySummary() {
        assertEquals(listOf("env"), filterCommands(commands, "环境").map { it.name })
    }

    @Test
    fun capsAtEight() {
        val many = (1..20).map { CommandInfo("cmd$it", emptyList(), "第 $it 条", "") }
        assertEquals(8, filterCommands(many, "").size)
        assertTrue(filterCommands(many, "cmd1").size <= 8)
    }

    @Test
    fun parsesSlashQuery() {
        assertEquals("env", commandQuery("/ENV"))
        assertEquals("", commandQuery("/"))
        assertNull(commandQuery("hello"))
        assertNull(commandQuery("/env\nlist"))
    }
}
