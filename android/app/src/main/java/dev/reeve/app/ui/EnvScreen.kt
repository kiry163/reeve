package dev.reeve.app.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.outlined.Delete
import androidx.compose.material.icons.outlined.SmartToy
import androidx.compose.material3.Button
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
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
import dev.reeve.app.core.BotRef
import dev.reeve.app.core.BotSession
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

/** 两层环境变量：实例级（所有 Bot 共用）与当前 Bot 级。保存时整层替换。 */
@Composable
fun EnvScreen(state: AppState, initial: BotRef?, nav: Nav) {
    val scope = rememberCoroutineScope()
    var selected by remember(initial) { mutableStateOf(initial ?: defaultBotRef(state)) }
    val session = selected?.let { state.session(it) }
    // 实例级变量属于服务器实例：选中 Bot 时只能取同一台服务器上的会话，
    // 取不到就禁用实例级编辑，绝不退到别的服务器。
    val selectedRef = selected
    val instanceSession = if (selectedRef == null) {
        firstSession(state)
    } else {
        session ?: state.allRefs.firstOrNull { it.server == selectedRef.server }?.let { state.session(it) }
    }
    // 同一台服务器内换 Bot 不重读实例层；换服务器或会话从无到有才重新加载。
    val instanceKey = selectedRef?.server ?: instanceSession?.ref?.server
    val serverName = selectedRef?.let { state.server(it.server)?.name } ?: instanceSession?.server?.name

    // remember(key)：目标一变就同步清空草稿，旧目标的表格不会在新目标上显示或被保存。
    var instRows by remember(instanceKey) { mutableStateOf<List<EnvRow>>(emptyList()) }
    var instSaved by remember(instanceKey) { mutableStateOf<List<EnvRow>>(emptyList()) }
    var instLoaded by remember(instanceKey) { mutableStateOf(false) }
    var instOk by remember(instanceKey) { mutableStateOf(false) }
    var instBusy by remember(instanceKey) { mutableStateOf(false) }
    var instMsg by remember(instanceKey) { mutableStateOf<EnvMessage?>(null) }

    var botRows by remember(session?.ref) { mutableStateOf<List<EnvRow>>(emptyList()) }
    var botSaved by remember(session?.ref) { mutableStateOf<List<EnvRow>>(emptyList()) }
    var botLoaded by remember(session?.ref) { mutableStateOf(false) }
    var botOk by remember(session?.ref) { mutableStateOf(false) }
    var botBusy by remember(session?.ref) { mutableStateOf(false) }
    var botMsg by remember(session?.ref) { mutableStateOf<EnvMessage?>(null) }

    LaunchedEffect(instanceKey, instanceSession != null) {
        val s = instanceSession
        instLoaded = false
        instOk = false
        instMsg = null
        if (s == null) {
            instLoaded = true
            return@LaunchedEffect
        }
        runCatching { s.loadEnv("instance") }
            .onSuccess { instRows = envRows(it); instSaved = instRows; instOk = true }
            .onFailure { instMsg = EnvMessage(ServerClient.describe(it), error = true) }
        instLoaded = true
    }

    LaunchedEffect(session?.ref) {
        val s = session
        botLoaded = false
        botOk = false
        botMsg = null
        if (s == null) {
            botLoaded = true
            return@LaunchedEffect
        }
        runCatching { s.loadEnv("bot") }
            .onSuccess { botRows = envRows(it); botSaved = botRows; botOk = true }
            .onFailure { botMsg = EnvMessage(ServerClient.describe(it), error = true) }
        botLoaded = true
    }

    fun save(
        s: BotSession, layer: String, rows: List<EnvRow>,
        onSaved: (List<EnvRow>) -> Unit, onMsg: (EnvMessage) -> Unit, busy: (Boolean) -> Unit,
    ) {
        busy(true)
        scope.launch {
            runCatching { s.saveEnv(layer, envVars(rows)) }
                .onSuccess { res: JSONObject ->
                    val warnings = res.arr("warnings").strings()
                    val merged = envRows(envVars(rows))
                    onSaved(merged)
                    onMsg(
                        if (warnings.isEmpty()) EnvMessage("已保存。", error = false)
                        else EnvMessage(warnings.joinToString("\n"), error = false, warning = true),
                    )
                }
                .onFailure { onMsg(EnvMessage(ServerClient.describe(it), error = true)) }
            busy(false)
        }
    }

    SubPage("环境变量", nav) {
        Note("保存会用当前表格整体替换本层变量：表格里删掉的行会从服务器上删除。")
        SectionLabel(if (serverName.isNullOrEmpty()) "实例级（所有 Bot 共用）" else "实例级 · $serverName（所有 Bot 共用）")
        if (instanceSession == null) {
            Note(
                if (selectedRef != null) "这台服务器还没有会话，请先打开任意一个 Bot，再回来编辑实例级变量。"
                else "还没有可用的服务器，连接后才能读取实例级变量。",
                Reeve.extra.warn,
            )
        } else if (!instLoaded) {
            Busy()
        } else {
            if (instRows.isEmpty()) Note("还没有实例级变量。变量保存在服务器的 REEVE_HOME/env 文件（默认 ~/.reeve/env）。")
            EnvTable(
                rows = instRows,
                onChange = { i, row -> instRows = instRows.toMutableList().also { it[i] = row }; instMsg = null },
                onDelete = { i -> instRows = instRows.toMutableList().also { it.removeAt(i) }; instMsg = null },
                onAdd = { instRows = instRows + EnvRow("", ""); instMsg = null },
            )
            EnvFooter(
                dirty = envDirty(instRows, instSaved),
                canSave = canSaveEnv(instOk, instRows, instSaved),
                busy = instBusy,
                onReset = { instRows = instSaved; instMsg = null },
                onSave = {
                    instanceSession?.let { s ->
                        save(s, "instance", instRows, { rows -> instRows = rows; instSaved = rows }, { msg -> instMsg = msg }, { busy -> instBusy = busy })
                    }
                },
            )
            instMsg?.let { EnvFeedback(it) }
        }

        SectionLabel("当前 Bot")
        if (selected == null) {
            Note("还没有可设置的 Bot。先添加服务器。")
        } else {
            BotPickerRow(state, selected, onPick = { selected = it })
            if (session == null) {
                Note("这个 Bot 现在连不上，读取不了变量。")
            } else if (!botLoaded) {
                Busy()
            } else {
                Note("Bot 级变量只影响这个 Bot，同名时覆盖实例级。保存在服务器的 Bot 数据目录 env.json 里。")
                if (botRows.isEmpty()) Note("这个 Bot 还没有自己的变量。点下面的「新增变量」开始添加。")
                EnvTable(
                    rows = botRows,
                    onChange = { i, row -> botRows = botRows.toMutableList().also { it[i] = row }; botMsg = null },
                    onDelete = { i -> botRows = botRows.toMutableList().also { it.removeAt(i) }; botMsg = null },
                    onAdd = { botRows = botRows + EnvRow("", ""); botMsg = null },
                )
                EnvFooter(
                    dirty = envDirty(botRows, botSaved),
                    canSave = canSaveEnv(botOk, botRows, botSaved),
                    busy = botBusy,
                    onReset = { botRows = botSaved; botMsg = null },
                    onSave = {
                        save(session, "bot", botRows, { rows -> botRows = rows; botSaved = rows }, { msg -> botMsg = msg }, { busy -> botBusy = busy })
                    },
                )
                botMsg?.let { EnvFeedback(it) }
            }
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
        Button(enabled = canSave && !busy, onClick = onSave) { Text(if (busy) "正在保存…" else "保存这一层") }
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

/** 设置页共用的 Bot 选择行：换 Bot 只换作用域，不影响实例级设置。 */
@Composable
fun BotPickerRow(state: AppState, selected: BotRef?, onPick: (BotRef) -> Unit) {
    var open by remember { mutableStateOf(false) }
    val refs = state.allRefs
    Box {
        val current = selected?.let { state.session(it)?.profile?.name ?: state.server(it.server)?.profile(it)?.name }
        val server = selected?.let { state.server(it.server)?.name }
        val label = listOfNotNull(current, server?.takeIf { it.isNotEmpty() }).joinToString(" · ").ifEmpty { "没有可用的 Bot" }
        ListRow(
            "设置哪个 Bot", label, icon = Icons.Outlined.SmartToy,
            onClick = if (refs.isEmpty()) null else ({ open = true }),
        )
        DropdownMenu(open, { open = false }) {
            refs.forEach { ref ->
                val name = state.session(ref)?.profile?.name ?: state.server(ref.server)?.profile(ref)?.name ?: ref.bot
                val server = state.server(ref.server)?.name.orEmpty()
                DropdownMenuItem(
                    text = { Text(if (server.isEmpty()) name else "$name · $server") },
                    onClick = { onPick(ref); open = false },
                )
            }
        }
    }
}

fun defaultBotRef(state: AppState): BotRef? =
    state.allRefs.firstOrNull { state.session(it) != null } ?: state.allRefs.firstOrNull()

fun firstSession(state: AppState): BotSession? = state.allRefs.firstNotNullOfOrNull { state.session(it) }
