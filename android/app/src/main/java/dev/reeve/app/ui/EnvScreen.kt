package dev.reeve.app.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.outlined.Delete
import androidx.compose.material3.Button
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import dev.reeve.app.core.AppState
import dev.reeve.app.core.EnvRow
import dev.reeve.app.core.ServerClient
import dev.reeve.app.core.arr
import dev.reeve.app.core.canSaveEnv
import dev.reeve.app.core.envDirty
import dev.reeve.app.core.envRows
import dev.reeve.app.core.envVars
import dev.reeve.app.core.strings
import kotlinx.coroutines.launch
import org.json.JSONObject

private data class EnvMessage(val text: String, val error: Boolean, val warning: Boolean = false)

/** 实例级环境变量：所有 Bot 共用一份，保存时整表替换。 */
@Composable
fun EnvScreen(state: AppState, server: String, nav: Nav) {
    val scope = rememberCoroutineScope()
    val session = state.allRefs.firstNotNullOfOrNull { ref ->
        if (ref.server == server) state.session(ref) else null
    }
    val serverName = state.server(server)?.name

    var rows by remember(server) { mutableStateOf<List<EnvRow>>(emptyList()) }
    var saved by remember(server) { mutableStateOf<List<EnvRow>>(emptyList()) }
    var loaded by remember(server) { mutableStateOf(false) }
    var canEdit by remember(server) { mutableStateOf(false) }
    var busy by remember(server) { mutableStateOf(false) }
    var msg by remember(server) { mutableStateOf<EnvMessage?>(null) }

    LaunchedEffect(server, session?.ref) {
        loaded = false
        canEdit = false
        msg = null
        val s = session
        if (s == null) {
            loaded = true
            return@LaunchedEffect
        }
        runCatching { s.loadEnv() }
            .onSuccess { rows = envRows(it); saved = rows; canEdit = true }
            .onFailure { msg = EnvMessage(ServerClient.describe(it), error = true) }
        loaded = true
    }

    fun save() {
        val s = session ?: return
        busy = true
        scope.launch {
            runCatching { s.saveEnv(envVars(rows)) }
                .onSuccess { res: JSONObject ->
                    val warnings = res.arr("warnings").strings()
                    val merged = envRows(envVars(rows))
                    saved = merged
                    msg = if (warnings.isEmpty()) EnvMessage("已保存。", error = false)
                    else EnvMessage(warnings.joinToString("\n"), error = false, warning = true)
                }
                .onFailure { msg = EnvMessage(ServerClient.describe(it), error = true) }
            busy = false
        }
    }

    SubPage("环境变量", nav) {
        Note(
            "实例级变量，所有 Bot 共用，会注入每个任务和命令。注册成常驻服务时，" +
                "服务器会从当前 shell 导入 PATH、时区/语言、代理与 TLS 证书设置；" +
                "之后可用 reeve service env refresh 更新。保存会用当前表格整体替换。",
        )
        SectionLabel(if (serverName.isNullOrEmpty()) "实例级" else "实例级 · $serverName（所有 Bot 共用）")
        if (session == null) {
            Note("这台服务器还没有登录，请先打开任意一个 Bot，再回来编辑。", Reeve.extra.warn)
        } else if (!loaded) {
            Busy()
        } else {
            if (rows.isEmpty()) Note("还没有实例级变量。变量保存在服务器的 REEVE_HOME/env 文件（默认 ~/.reeve/env）。")
            EnvTable(
                rows = rows,
                onChange = { i, row -> rows = rows.toMutableList().also { it[i] = row }; msg = null },
                onDelete = { i -> rows = rows.toMutableList().also { it.removeAt(i) }; msg = null },
                onAdd = { rows = rows + EnvRow("", ""); msg = null },
            )
            EnvFooter(
                dirty = envDirty(rows, saved),
                canSave = canSaveEnv(canEdit, rows, saved),
                busy = busy,
                onReset = { rows = saved; msg = null },
                onSave = { save() },
            )
            msg?.let { EnvFeedback(it) }
        }
    }
}

@Composable
private fun EnvTable(rows: List<EnvRow>, onChange: (Int, EnvRow) -> Unit, onDelete: (Int) -> Unit, onAdd: () -> Unit) {
    rows.forEachIndexed { i, row ->
        Row(
            Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 3.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(6.dp),
        ) {
            OutlinedTextField(
                row.name, { onChange(i, row.copy(name = it)) }, label = { Text("名称") }, singleLine = true,
                placeholder = { Text("HTTPS_PROXY") }, keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Ascii),
                modifier = Modifier.weight(0.44f),
            )
            OutlinedTextField(
                row.value, { onChange(i, row.copy(value = it)) }, label = { Text("值") }, singleLine = true,
                modifier = Modifier.weight(0.56f),
            )
            IconButton(onClick = { onDelete(i) }) {
                Icon(Icons.Outlined.Delete, "删除 ${row.name.trim().ifEmpty { "这一行" }}")
            }
        }
    }
    TextButton(onClick = onAdd, modifier = Modifier.padding(horizontal = 8.dp)) {
        Icon(Icons.Filled.Add, null)
        Text("新增变量")
    }
}

@Composable
private fun EnvFooter(dirty: Boolean, canSave: Boolean, busy: Boolean, onReset: () -> Unit, onSave: () -> Unit) {
    Row(Modifier.padding(horizontal = 16.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        if (dirty) OutlinedButton(enabled = !busy, onClick = onReset) { Text("还原") }
        Button(enabled = canSave && !busy, onClick = onSave) { Text(if (busy) "正在保存…" else "保存") }
    }
}

@Composable
private fun EnvFeedback(msg: EnvMessage) {
    val color = when {
        msg.error -> MaterialTheme.colorScheme.error
        msg.warning -> Reeve.extra.warn
        else -> Reeve.extra.ok
    }
    Note(msg.text, color)
}
