package dev.reeve.app.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import dev.reeve.app.core.AgentConnections
import dev.reeve.app.core.ApiChannel
import dev.reeve.app.core.AppState
import dev.reeve.app.core.BotRef
import dev.reeve.app.core.CliAvailability
import dev.reeve.app.core.CliOptions
import dev.reeve.app.core.CliStatus
import dev.reeve.app.core.ExecutorsConfig
import dev.reeve.app.core.ServerClient
import dev.reeve.app.core.cliAvailability
import dev.reeve.app.core.cliSelectable
import kotlinx.coroutines.launch

private data class ExecMessage(val text: String, val error: Boolean = false)

/** 执行器白名单、默认执行器与每个外部 CLI 的模型/认证选项。 */
@Composable
fun ExecutorsScreen(state: AppState, initial: BotRef?, nav: Nav) {
    val scope = rememberCoroutineScope()
    var selected by remember(initial) { mutableStateOf(initial ?: defaultBotRef(state)) }
    val session = selected?.let { state.session(it) }

    var config by remember { mutableStateOf<ExecutorsConfig?>(null) }
    var saved by remember { mutableStateOf<ExecutorsConfig?>(null) }
    var conn by remember { mutableStateOf<AgentConnections?>(null) }
    var channels by remember { mutableStateOf<List<ApiChannel>>(emptyList()) }
    var busy by remember { mutableStateOf(false) }
    var detecting by remember { mutableStateOf(false) }
    var msg by remember { mutableStateOf<ExecMessage?>(null) }

    LaunchedEffect(session?.ref) {
        val s = session ?: return@LaunchedEffect
        config = null; saved = null; conn = null; msg = null
        runCatching { s.loadExecutors() }
            .onSuccess { config = it; saved = it }
            .onFailure { msg = ExecMessage(ServerClient.describe(it), error = true) }
        // probe=0：只读安装状态与缓存，打开页面不反复起子进程。
        runCatching { s.loadConnections(probe = false) }
            .onSuccess { conn = it }
            .onFailure { if (msg == null) msg = ExecMessage(ServerClient.describe(it), error = true) }
        channels = runCatching { s.loadChannels().providers }.getOrDefault(emptyList())
    }

    fun refreshConnections(probe: Boolean) {
        val s = session ?: return
        if (probe) detecting = true
        scope.launch {
            runCatching { s.loadConnections(probe = probe) }
                .onSuccess { conn = it }
                .onFailure { msg = ExecMessage(ServerClient.describe(it), error = true) }
            detecting = false
        }
    }

    SubPage(
        "执行器", nav, subtitle = selected?.let { state.session(it)?.profile?.name ?: it.bot },
        actions = {
            TextButton(enabled = !detecting && session != null, onClick = { refreshConnections(true) }) {
                Text(if (detecting) "正在检测…" else "重新检测")
            }
        },
    ) {
        BotPickerRow(state, selected, onPick = { selected = it; msg = null })
        val c = config
        val connections = conn
        if (session == null) {
            Note("这个 Bot 现在连不上，读取不了执行器设置。")
            return@SubPage
        }
        if (c == null || connections == null) {
            if (msg == null) Busy() else Note(msg!!.text, MaterialTheme.colorScheme.error)
            return@SubPage
        }

        Note("白名单决定任务可以交给哪些执行器；新任务默认用下面选中的一个。检测的是 reeve 所在服务器上的安装情况。")

        SectionLabel("白名单")
        ListRow("内置执行器", "内置执行器始终可用，不能取消") {
            Checkbox(checked = true, onCheckedChange = null)
        }
        connections.clis.forEach { cli ->
            val on = cli.id in c.allowed
            val usable = cliSelectable(on, cli.probed, cli.available)
            val unavailable = cliAvailability(cli.probed, cli.available) == CliAvailability.Unavailable
            ListRow(
                cli.name,
                cliStatusText(cli),
                titleColor = if (unavailable && !on) MaterialTheme.colorScheme.onSurfaceVariant else MaterialTheme.colorScheme.onSurface,
            ) {
                Checkbox(
                    checked = on,
                    enabled = usable && !busy,
                    onCheckedChange = { checked ->
                        val allowed = if (checked) (c.allowed + cli.id).distinct() else c.allowed.filter { it != cli.id }
                        val def = if (!checked && c.default == cli.id) "builtin" else c.default
                        config = c.copy(allowed = allowed, default = def)
                        msg = null
                    },
                )
            }
            if (!cli.note.isNullOrEmpty()) {
                Note(cli.note!!, if (unavailable) Reeve.extra.warn else MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
        if (c.allowed.any { it != "builtin" }) {
            Note("外部 CLI 等于把整台机器的执行权交给它，只勾选你信任的 CLI。", Reeve.extra.warn)
        }

        SectionLabel("默认执行器")
        c.allowed.forEach { id ->
            val cli = connections.clis.firstOrNull { it.id == id }
            val label = if (id == "builtin") "内置执行器" else cli?.name ?: id
            ListRow(
                label, if (id == "builtin") "用 API 渠道里的任务模型" else "外部 CLI · 任务整段在本机执行",
                onClick = { config = c.copy(default = id); msg = null },
                leading = { RadioButton(selected = c.default == id, onClick = null) },
            )
        }
        Note("默认执行器只列白名单里的项。聊天里也可以让任务换用白名单里的其他执行器。")

        val externals = c.allowed.filter { it != "builtin" }
        if (externals.isNotEmpty()) SectionLabel("外部 CLI 的选项")
        externals.forEach { id ->
            val cli = connections.clis.firstOrNull { it.id == id }
            val opts = c.clis[id] ?: CliOptions()
            CliOptionsEditor(
                cli = cli, id = id, opts = opts, channels = channels,
                onChange = { next -> config = c.copy(clis = c.clis + (id to next)); msg = null },
            )
        }

        msg?.let { Note(it.text, if (it.error) MaterialTheme.colorScheme.error else Reeve.extra.ok) }
        val dirty = saved?.let { c.signature() != it.signature() } ?: true
        Row(Modifier.padding(16.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            if (dirty) OutlinedButton(enabled = !busy && saved != null, onClick = { config = saved; msg = null }) { Text("还原") }
            Button(
                enabled = dirty && !busy && saved != null,
                onClick = {
                    busy = true; msg = null
                    scope.launch {
                        runCatching { session.saveExecutors(c) }
                            .onSuccess {
                                config = it; saved = it
                                msg = ExecMessage("已保存，下一条消息和新任务生效。")
                                conn = runCatching { session.loadConnections(probe = false) }.getOrNull()
                            }
                            .onFailure { msg = ExecMessage(ServerClient.describe(it), error = true) }
                        busy = false
                    }
                },
            ) { Text(if (busy) "正在保存…" else "保存执行器设置") }
        }
    }
}

private fun cliStatusText(cli: CliStatus): String {
    val availability = when (cli.availability) {
        CliAvailability.Unknown -> "未检测"
        CliAvailability.Available -> "可用"
        CliAvailability.Unavailable -> "不可用"
    }
    val installed = if (cli.installed) "已安装" else "未安装"
    val version = if (cli.version.isNotEmpty()) "v${cli.version}" else "版本未知"
    return listOf(cli.id, installed, version, availability).joinToString(" · ")
}

@Composable
private fun CliOptionsEditor(
    cli: CliStatus?, id: String, opts: CliOptions, channels: List<ApiChannel>, onChange: (CliOptions) -> Unit,
) {
    val name = cli?.name ?: id
    SectionLabel("$name 的选项")
    if (id == "pi") {
        Note("Pi 通过 reeve 的本机网关调用选中的 API 渠道和模型，渠道密钥留在 reeve 里。", MaterialTheme.colorScheme.onSurfaceVariant)
        PiRoutePicker(opts, channels, onChange)
        return
    }
    if (cli?.modelOption != false) {
        OutlinedTextField(
            opts.model, { onChange(opts.copy(model = it)) }, label = { Text("模型") }, singleLine = true,
            placeholder = { Text("留空使用 $name 的默认模型") },
            keyboardOptions = KeyboardOptions.Default,
            modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp),
        )
        if (!cli?.models.isNullOrEmpty()) Note("可填 ${cli.models.joinToString(" / ")} 或完整模型 ID。")
    }
    var authOpen by remember { mutableStateOf(false) }
    val auth = opts.auth.ifEmpty { "subscription" }
    val authLabel = when (auth) { "env" -> "沿用进程环境变量"; else -> "官方订阅" }
    Box {
        ListRow("认证", authLabel, onClick = { authOpen = true })
        DropdownMenu(authOpen, { authOpen = false }) {
            DropdownMenuItem(text = { Text("官方订阅") }, onClick = { onChange(opts.copy(auth = "subscription")); authOpen = false })
            DropdownMenuItem(text = { Text("沿用进程环境变量") }, onClick = { onChange(opts.copy(auth = "env")); authOpen = false })
        }
    }
    Note(
        if (auth == "env") "保留 reeve 进程里的 API Key / Base URL 环境变量，由 CLI 自行决定认证方式。"
        else "只用 CLI 自己登录的官方账户；即使进程里设置了 API Key 环境变量也不会改走按量计费。",
    )
}

/** Pi 固定走 channel 认证：只需选渠道和模型。 */
@Composable
private fun PiRoutePicker(opts: CliOptions, channels: List<ApiChannel>, onChange: (CliOptions) -> Unit) {
    var channelOpen by remember { mutableStateOf(false) }
    val current = channels.firstOrNull { it.id == opts.channel }
    Box {
        ListRow("API 渠道", current?.name ?: if (opts.channel.isEmpty()) "未选择" else opts.channel, onClick = { channelOpen = true })
        DropdownMenu(channelOpen, { channelOpen = false }) {
            channels.filter { it.enabled }.forEach { ch ->
                DropdownMenuItem(text = { Text(ch.name) }, onClick = {
                    val model = if (opts.model in ch.visibleModels) opts.model else ch.visibleModels.firstOrNull().orEmpty()
                    onChange(opts.copy(auth = "channel", channel = ch.id, model = model))
                    channelOpen = false
                })
            }
        }
    }
    var modelOpen by remember { mutableStateOf(false) }
    Box {
        ListRow("模型", opts.model.ifEmpty { "未选择" }, onClick = { modelOpen = true })
        DropdownMenu(modelOpen, { modelOpen = false }) {
            current?.visibleModels?.forEach { m ->
                DropdownMenuItem(
                    text = { Text(m, fontSize = 14.sp) },
                    leadingIcon = { RadioButton(selected = m == opts.model, onClick = null) },
                    onClick = { onChange(opts.copy(model = m)); modelOpen = false },
                )
            }
        }
    }
    OutlinedTextField(
        opts.model, { onChange(opts.copy(model = it)) }, label = { Text("模型 ID") }, singleLine = true,
        modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp),
    )
    if (current == null && opts.channel.isNotEmpty()) {
        Note("之前选的渠道已被停用或删除，请重新选择。", Reeve.extra.warn)
    }
}
