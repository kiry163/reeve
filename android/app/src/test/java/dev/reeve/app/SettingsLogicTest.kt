package dev.reeve.app

import dev.reeve.app.core.CliAvailability
import dev.reeve.app.core.CliOptions
import dev.reeve.app.core.EnvRow
import dev.reeve.app.core.canSaveEnv
import dev.reeve.app.core.cliAvailability
import dev.reeve.app.core.cliSelectable
import dev.reeve.app.core.envBody
import dev.reeve.app.core.envDirty
import dev.reeve.app.core.envRows
import dev.reeve.app.core.envVars
import dev.reeve.app.core.executorPayload
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class SettingsLogicTest {

    // ---------- 环境变量表格 ----------

    @Test
    fun envVarsKeepsRowsAndTrimsNames() {
        val vars = envVars(listOf(EnvRow(" HTTPS_PROXY ", "http://127.0.0.1:7890"), EnvRow("NO_COLOR", "1")))
        assertEquals(mapOf("HTTPS_PROXY" to "http://127.0.0.1:7890", "NO_COLOR" to "1"), vars)
    }

    @Test
    fun envVarsLastDuplicateWins() {
        val vars = envVars(listOf(EnvRow("A", "1"), EnvRow("A", "2"), EnvRow(" B ", "3"), EnvRow("B", "4")))
        assertEquals(mapOf("A" to "2", "B" to "4"), vars)
    }

    @Test
    fun envVarsSkipsBlankNamesAndKeepsBlankValues() {
        val vars = envVars(listOf(EnvRow("", "orphan"), EnvRow("  ", "x"), EnvRow("EMPTY", "")))
        assertEquals(mapOf("EMPTY" to ""), vars)
    }

    @Test
    fun envVarsUpdatedTableDropsDeletedRows() {
        val saved = envVars(listOf(EnvRow("A", "1"), EnvRow("B", "2")))
        val edited = listOf(EnvRow("A", "1"), EnvRow("C", "3"))
        // 整层替换：删除的行不在新表里，PUT body 里也确实不存在。
        assertEquals(setOf("A", "B"), saved.keys)
        val body = envBody(envVars(edited))
        val vars = body.getJSONObject("vars")
        assertEquals(setOf("A", "C"), vars.keys().asSequence().toSet())
        assertFalse(vars.has("B"))
        assertEquals("1", vars.getString("A"))
        assertEquals("3", vars.getString("C"))
    }

    @Test
    fun envRowsSortsByName() {
        assertEquals(listOf(EnvRow("A", "1"), EnvRow("B", "2")), envRows(mapOf("B" to "2", "A" to "1")))
    }

    @Test
    fun newTargetStartsEmptyAndCannotSave() {
        // 切换目标（session/服务器变化）后 remember 会清空 rows 与 saved；
        // 加载失败时 loaded/loadOk 为 false，空草稿不能被保存。
        val rows = emptyList<EnvRow>()
        val saved = emptyList<EnvRow>()
        assertFalse(envDirty(rows, saved))
        assertFalse(canSaveEnv(loaded = false, rows = rows, saved = saved))
        assertFalse(canSaveEnv(loaded = true, rows = rows, saved = saved))
        // 清空整层仍然允许保存（空表 + 旧表非空）。
        assertTrue(canSaveEnv(loaded = true, rows = emptyList(), saved = listOf(EnvRow("A", "1"))))
    }

    // ---------- 执行器草稿 → PUT 体 ----------

    @Test
    fun executorPayloadAlwaysIncludesBuiltinFirst() {
        val body = executorPayload(listOf("omp"), "omp", mapOf("omp" to CliOptions(model = "gpt-5")))
        assertEquals(listOf("builtin", "omp"), body.getJSONArray("allowed").toList())
        assertEquals("omp", body.getString("default"))
    }

    @Test
    fun executorPayloadFallsBackToBuiltinForMissingDefault() {
        val body = executorPayload(listOf("builtin"), "omp", emptyMap())
        assertEquals(listOf("builtin"), body.getJSONArray("allowed").toList())
        assertEquals("builtin", body.getString("default"))
    }

    @Test
    fun executorPayloadKeepsOptionsOfUncheckedCli() {
        val clis = mapOf(
            "omp" to CliOptions(model = "gpt-5", auth = "subscription"),
            "pi" to CliOptions(model = "m1", auth = "channel", channel = "ch"),
        )
        // 只勾了 builtin：取消勾选的 CLI 仍要保留 clis，重新勾选不用重填。
        val body = executorPayload(listOf("builtin"), "builtin", clis)
        assertEquals(listOf("builtin"), body.getJSONArray("allowed").toList())
        val saved = body.getJSONObject("clis")
        assertEquals("gpt-5", saved.getJSONObject("omp").getString("cli_model"))
        assertEquals("subscription", saved.getJSONObject("omp").getString("cli_auth"))
        assertEquals("m1", saved.getJSONObject("pi").getString("cli_model"))
        assertEquals("channel", saved.getJSONObject("pi").getString("cli_auth"))
        assertEquals("ch", saved.getJSONObject("pi").getString("cli_channel"))
    }

    @Test
    fun executorPayloadNormalizesPiAndBlankAuth() {
        val body = executorPayload(
            listOf("builtin", "pi"), "pi",
            mapOf(
                "pi" to CliOptions(model = " m ", channel = " ch "),
                "omp" to CliOptions(model = "x", auth = ""),
            ),
        )
        val clis = body.getJSONObject("clis")
        assertEquals("channel", clis.getJSONObject("pi").getString("cli_auth"))
        assertEquals("m", clis.getJSONObject("pi").getString("cli_model"))
        assertEquals("ch", clis.getJSONObject("pi").getString("cli_channel"))
        assertEquals("subscription", clis.getJSONObject("omp").getString("cli_auth"))
        // 没有勾选 omp，但选项仍保留。
        assertFalse(body.getJSONArray("allowed").toList().contains("omp"))
    }

    // ---------- available/probed 三态 ----------

    @Test
    fun availabilityIsUnknownWithoutProbe() {
        assertEquals(CliAvailability.Unknown, cliAvailability(probed = false, available = false))
        assertTrue(cliSelectable(checked = false, probed = false, available = false))
    }

    @Test
    fun availabilityIsAvailableWhenProbeSucceeds() {
        assertEquals(CliAvailability.Available, cliAvailability(probed = true, available = true))
        assertTrue(cliSelectable(checked = false, probed = true, available = true))
    }

    @Test
    fun availabilityIsUnavailableWhenProbeFails() {
        assertEquals(CliAvailability.Unavailable, cliAvailability(probed = true, available = false))
        assertFalse(cliSelectable(checked = false, probed = true, available = false))
        // 已经勾选的项仍可取消，避免配置卡死。
        assertTrue(cliSelectable(checked = true, probed = true, available = false))
    }

    @Test
    fun payloadIsValidJsonShape() {
        val body = executorPayload(listOf("builtin", "builtin", "omp"), "omp", emptyMap())
        assertTrue(body.has("allowed"))
        assertTrue(body.has("default"))
        assertTrue(body.has("clis"))
        assertEquals(JSONObject().toString(), body.getJSONObject("clis").toString())
    }
}
