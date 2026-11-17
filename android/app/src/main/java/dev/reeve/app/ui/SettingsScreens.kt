package dev.reeve.app.ui

import androidx.compose.ui.draw.clip
import android.content.Intent
import android.net.Uri
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.outlined.AccountCircle
import androidx.compose.material.icons.outlined.Cable
import androidx.compose.material.icons.outlined.Delete
import androidx.compose.material.icons.outlined.Dns
import androidx.compose.material.icons.outlined.Memory
import androidx.compose.material.icons.automirrored.outlined.MenuBook
import androidx.compose.material.icons.outlined.Notifications
import androidx.compose.material.icons.outlined.NotificationsActive
import androidx.compose.material.icons.outlined.OpenInBrowser
import androidx.compose.material.icons.outlined.Palette
import androidx.compose.material.icons.outlined.SmartToy
import androidx.compose.material.icons.outlined.SystemUpdate
import androidx.compose.material.icons.outlined.Terminal
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import dev.reeve.app.BuildConfig
import dev.reeve.app.KeepAliveService
import dev.reeve.app.R
import dev.reeve.app.core.AgentConnections
import dev.reeve.app.core.ApiChannel
import dev.reeve.app.core.ApiSettings
import dev.reeve.app.core.AppState
import dev.reeve.app.core.BotRef
import dev.reeve.app.core.BotSession
import dev.reeve.app.core.ChannelUse
import dev.reeve.app.core.ServerClient
import dev.reeve.app.core.ServerEntry
import dev.reeve.app.core.ServerStatus
import dev.reeve.app.core.str
import kotlinx.coroutines.launch

private fun statusText(s: ServerEntry): String = when (val st = s.status) {
    ServerStatus.Online -> {
        val n = s.bots.size
        "在线 · $n 个 Bot" + if (s.config.insecure) " · HTTP 未加密" else ""
    }
    ServerStatus.Connecting -> "正在连接"
    ServerStatus.NeedsPairing -> "token 已失效，需要重新配对"
    is ServerStatus.Offline -> "离线：${st.error}"
}

// ---------- 设置首页 ----------

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SettingsScreen(state: AppState, nav: Nav) {
    val context = LocalContext.current
    Scaffold(topBar = { TopAppBar(title = { Text("设置") }) }, containerColor = MaterialTheme.colorScheme.surface) { pad ->
        Column(Modifier.padding(pad).fillMaxSize().verticalScroll(rememberScrollState())) {
            SectionLabel("服务器")
            state.servers.forEach { s ->
                ListRow(s.name, statusText(s), Icons.Outlined.Dns, chevron = true, onClick = { nav.push(Screen.Server(s.id)) })
            }
            ListRow("添加服务器", "扫码、粘贴配对串或手填地址 + token", Icons.Filled.Add, titleColor = MaterialTheme.colorScheme.primary, onClick = { nav.push(Screen.AddServer()) })
            SectionLabel("Bot 设置")
            ListRow("环境变量", "实例级与当前 Bot 的变量", Icons.Outlined.Terminal, chevron = true, onClick = { nav.push(Screen.Env()) })
            ListRow("执行器", "白名单、默认执行器与外部 CLI", Icons.Outlined.Memory, chevron = true, onClick = { nav.push(Screen.Executors()) })
            ListRow("技能", "服务器加载的技能索引", Icons.AutoMirrored.Outlined.MenuBook, chevron = true, onClick = { nav.push(Screen.Skills()) })
            SectionLabel("通用")
            SwitchRow(
                "后台保持连接", "用一条常驻通知保持连接，审批和任务结束能马上提醒你", Icons.Outlined.NotificationsActive, state.keepAlive,
            ) { on ->
                state.changeKeepAlive(on)
                if (on) runCatching { KeepAliveService.start(context) } else KeepAliveService.stop(context)
            }
            ListRow("通知", "等你确认、任务结束、新回复", Icons.Outlined.Notifications, chevron = true, onClick = { nav.push(Screen.Notifications) })
            ListRow("外观", when (state.appearance) { "light" -> "浅色"; "dark" -> "深色"; else -> "跟随系统" }, Icons.Outlined.Palette, chevron = true,
                onClick = { nav.push(Screen.Appearance) })
            SectionLabel("客户端")
            ListRow("${context.getString(R.string.app_name)} ${BuildConfig.VERSION_NAME}", "版本号")
        }
    }
}

@Composable
fun AppearanceScreen(state: AppState, nav: Nav) {
    SubPage("外观", nav) {
        listOf("system" to "跟随系统", "light" to "浅色", "dark" to "深色").forEach { (k, v) ->
            ListRow(v, onClick = { state.changeAppearance(k) }, leading = { RadioButton(selected = state.appearance == k, onClick = null) })
        }
    }
}

@Composable
fun NotificationsScreen(state: AppState, nav: Nav) {
    var a by remember { mutableStateOf(state.prefs.notifyApproval) }
    var t by remember { mutableStateOf(state.prefs.notifyTask) }
    var r by remember { mutableStateOf(state.prefs.notifyReply) }
    SubPage("通知", nav) {
        SectionLabel("提醒我")
        SwitchRow("有操作需要我确认", "审批请求，独立一条通知，点开就能处理", checked = a) { a = it; state.prefs.notifyApproval = it }
        SwitchRow("任务完成、失败或取消", "任务结束时的结果", checked = t) { t = it; state.prefs.notifyTask = it }
        SwitchRow("新回复", "助手发来新消息时的摘要", checked = r) { r = it; state.prefs.notifyReply = it }
        Note("只在这个 Bot 的页面里不提醒（聊天、渠道、环境变量、执行器、技能、编辑）；前台看别的页面也会提醒。想在后台及时收到，打开「设置 › 后台保持连接」。通知样式在系统设置里调整。")
    }
}

// ---------- 一台服务器 ----------

@Composable
fun ServerScreen(state: AppState, s: ServerEntry, nav: Nav) {
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    val refs = s.bots.map { BotRef(s.id, it.id) }
    var selected by remember { mutableStateOf(refs.firstOrNull()) }
    val ref = selected?.takeIf { it in refs } ?: refs.firstOrNull()
    val session = ref?.let { state.session(it) }
    var settings by remember { mutableStateOf<ApiSettings?>(null) }
    var conn by remember { mutableStateOf<AgentConnections?>(null) }
    var picker by remember { mutableStateOf(false) }
    var confirmRemove by remember { mutableStateOf(false) }
    var update by remember { mutableStateOf<String?>(null) }

    LaunchedEffect(ref) {
        settings = null; conn = null
        if (session != null) {
            settings = runCatching { session.loadSettings() }.getOrNull()
            conn = runCatching { session.loadConnections() }.getOrNull()
        }
    }

    SubPage(s.name, nav, actions = {
        IconButton(onClick = { runCatching { context.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(s.config.url))) } }) {
            Icon(Icons.Outlined.OpenInBrowser, "在浏览器中打开")
        }
    }) {
        SectionLabel("连接")
        ListRow("地址", s.config.url)
        ListRow("状态", statusText(s) + (session?.info?.version?.let { " · reeve $it" } ?: ""))
        if (s.config.insecure) Note("明文 HTTP：token 和聊天内容在网络上不加密，建议给服务器配域名开启 HTTPS。", Reeve.extra.warn)
        ListRow(
            if (s.needsPairing) "重新配对" else "配对",
            if (s.needsPairing) "token 已失效" else "token 由配对串提供",
            Icons.Outlined.AccountCircle, chevron = true, onClick = { nav.push(Screen.AddServer(s.id)) },
        )

        SectionLabel("Bot 的模型和执行器")
        if (session == null) {
            Note(if (s.needsPairing) "重新配对后才能读取设置。" else "这台服务器上还没有可设置的 Bot。")
        } else {
            Box {
                ListRow("设置哪个 Bot", session.profile.name, Icons.Outlined.SmartToy, onClick = { picker = true })
                DropdownMenu(picker, { picker = false }) {
                    refs.forEach { r ->
                        val ss = state.session(r) ?: return@forEach
                        DropdownMenuItem(text = { Text(ss.profile.name) }, onClick = { selected = r; picker = false })
                    }
                }
            }
            val st = settings
            val chat = st?.let { x -> if (x.envOverride) "由环境变量指定" else listOfNotNull(x.channel(x.chatChannel)?.name, x.chatModel.ifEmpty { null }).joinToString(" · ").ifEmpty { "未选择" } } ?: "—"
            val agent = if (!conn?.cli.isNullOrEmpty()) "由 ${conn!!.clis.firstOrNull { it.id == conn!!.cli }?.name ?: conn!!.cli} 执行"
            else st?.let { x -> listOfNotNull(x.channel(x.agentChannel)?.name, x.agentModel.ifEmpty { null }).joinToString(" · ").ifEmpty { "未选择" } } ?: "—"
            ListRow("聊天模型", chat, chevron = true, onClick = { nav.push(Screen.BotEditor(s.id, session.ref)) })
            ListRow("任务模型", agent, chevron = true, onClick = { nav.push(Screen.BotEditor(s.id, session.ref)) })
            ListRow("API 渠道", st?.let { "${it.channels.size} 个渠道" } ?: "—", Icons.Outlined.Cable, chevron = true, onClick = { nav.push(Screen.Channels(session.ref)) })
            ListRow("执行器", "白名单、默认执行器与外部 CLI", Icons.Outlined.SmartToy, chevron = true, onClick = { nav.push(Screen.Executors(session.ref)) })
            ListRow("环境变量", "实例级与这个 Bot 的变量", Icons.Outlined.Terminal, chevron = true, onClick = { nav.push(Screen.Env(session.ref)) })
            ListRow("技能", "这个 Bot 加载的技能索引", Icons.AutoMirrored.Outlined.MenuBook, chevron = true, onClick = { nav.push(Screen.Skills(session.ref)) })
            Note("API 渠道整个 reeve 共用；聊天模型、任务模型、执行器和环境变量每个 Bot 各自设置，互不影响。")
        }

        HorizontalDivider(Modifier.padding(vertical = 8.dp))
        ListRow("检查更新", update, Icons.Outlined.SystemUpdate, onClick = {
            update = "正在检查…"
            scope.launch {
                update = runCatching { state.checkUpdate(s) }.fold({ j ->
                    when (j.str("state")) {
                        "available" -> "有新版本 ${j.str("latest")}（当前 ${j.str("current")}）"
                        "current" -> "已是最新（${j.str("current")}）"
                        else -> "当前 ${j.str("current").ifEmpty { "版本未知" }}"
                    }
                }, { "检查失败：${ServerClient.describe(it)}" })
            }
        })
        ListRow("从这台手机移除", icon = Icons.Outlined.Delete, titleColor = MaterialTheme.colorScheme.error, onClick = { confirmRemove = true })
    }

    if (confirmRemove) {
        AlertDialog(
            onDismissRequest = { confirmRemove = false },
            title = { Text("移除「${s.name}」？") },
            text = { Text("只是从这台手机移除并退出登录，服务器上的 Bot、任务和数据都不受影响。") },
            confirmButton = { TextButton(onClick = { confirmRemove = false; state.remove(s); nav.pop() }) { Text("移除", color = MaterialTheme.colorScheme.error) } },
            dismissButton = { TextButton(onClick = { confirmRemove = false }) { Text("取消") } },
        )
    }
}

// ---------- API 渠道（整个 reeve 共用） ----------

@Composable
fun ChannelsScreen(session: BotSession, nav: Nav) {
    val scope = rememberCoroutineScope()
    var saved by remember { mutableStateOf<ApiSettings?>(null) }
    var draft by remember { mutableStateOf<ApiSettings?>(null) }
    var usage by remember { mutableStateOf<List<ChannelUse>>(emptyList()) }
    var editing by remember { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }
    var msg by remember { mutableStateOf<Pair<String, Boolean>?>(null) }

    LaunchedEffect(session.ref) {
        runCatching {
            // 渠道池是实例级的；路由（本 Bot 用哪个渠道的哪个模型）走 B/settings。
            val channels = session.loadChannels()
            val routes = session.loadSettings()
            usage = channels.usage
            routes.copy(channels = channels.providers)
        }.onSuccess { saved = it; draft = it }.onFailure { msg = ServerClient.describe(it) to true }
    }
    val d = draft
    val editId = editing
    if (d != null && editId != null && d.channel(editId) != null) {
        ChannelEditor(session, d.channel(editId)!!, onDone = { ch ->
            draft = d.copy(channels = d.channels.map { if (it.id == ch.id) ch else it })
            editing = null
        }, onRemove = {
            val rest = d.channels.filter { it.id != editId }
            val next = rest.firstOrNull { it.enabled }
            draft = d.copy(
                channels = rest,
                chatChannel = if (d.chatChannel == editId) next?.id ?: "" else d.chatChannel,
                chatModel = if (d.chatChannel == editId) next?.visibleModels?.firstOrNull() ?: "" else d.chatModel,
                agentChannel = if (d.agentChannel == editId) next?.id ?: "" else d.agentChannel,
                agentModel = if (d.agentChannel == editId) next?.visibleModels?.firstOrNull() ?: "" else d.agentModel,
            )
            editing = null
        }, onCancel = { editing = null })
        return
    }

    SubPage("API 渠道", nav, subtitle = session.profile.name, actions = {
        IconButton(enabled = d != null, onClick = {
            val cur = draft ?: return@IconButton
            var n = cur.channels.size + 1
            while (cur.channel("channel-$n") != null) n++
            val ch = ApiChannel("channel-$n", "新渠道", "", enabled = true, authRequired = true)
            draft = cur.copy(
                channels = cur.channels + ch,
                chatChannel = cur.chatChannel.ifEmpty { ch.id }, agentChannel = cur.agentChannel.ifEmpty { ch.id },
            )
            editing = ch.id
        }) { Icon(Icons.Filled.Add, "添加渠道") }
    }) {
        Note("这些 API 渠道由这台 reeve 上的所有 Bot 共用，密钥加密保存在服务器上。下面的聊天模型和任务模型只对「${session.profile.name}」生效。保存后下一轮对话和任务生效。")
        if (d == null) {
            if (msg == null) Busy() else Note(msg!!.first, MaterialTheme.colorScheme.error)
            return@SubPage
        }
        if (d.envOverride) Note("当前模型由启动时的环境变量指定，优先于这里的设置。", Reeve.extra.warn)
        if (d.channels.isEmpty()) Note("还没有 API 渠道。点右上角 + 添加服务商或本地模型接口。")
        d.channels.forEach { ch ->
            val inUse = usage.count { it.chatProviderId == ch.id || it.agentProviderId == ch.id }
            ListRow(
                ch.name + if (ch.id == d.chatChannel) "（默认）" else "",
                (ch.baseUrl.ifEmpty { "还没填接口地址" }) + "\n${ch.visibleModels.size} 个可选模型 · " +
                    (if (ch.authRequired) "API Key" else "无需密钥") + if (inUse > 0) " · 被 $inUse 个 Bot 使用" else "",
                Icons.Outlined.Cable, onClick = { editing = ch.id },
            ) {
                Switch(checked = ch.enabled, onCheckedChange = { on -> draft = d.copy(channels = d.channels.map { if (it.id == ch.id) it.copy(enabled = on) else it }) })
            }
        }
        if (d.channels.isNotEmpty()) {
            SectionLabel("${session.profile.name} 用的模型")
            RoutePicker("聊天模型", d, d.chatChannel, d.chatModel) { c, m -> draft = d.copy(chatChannel = c, chatModel = m) }
            RoutePicker("任务模型（Agent API）", d, d.agentChannel, d.agentModel) { c, m -> draft = d.copy(agentChannel = c, agentModel = m) }
            Note("聊天模型负责回话和派活；任务模型给后端 Agent 用。在「执行器」里把默认执行器换成外部 CLI 时，任务改用 CLI 自己的账户和模型。")
        }
        msg?.let { Note(it.first, if (it.second) MaterialTheme.colorScheme.error else Reeve.extra.ok) }
        val valid = d.channels.isEmpty() || (
            d.channels.all { it.name.isNotBlank() && it.baseUrl.isNotBlank() } &&
                d.channel(d.chatChannel)?.let { it.enabled && d.chatModel in it.visibleModels } == true &&
                d.channel(d.agentChannel)?.let { it.enabled && d.agentModel in it.visibleModels } == true
            )
        Row(Modifier.padding(16.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            if (d != saved) OutlinedButton(onClick = { draft = saved; msg = null }) { Text("还原") }
            Button(enabled = !busy && d != saved && valid, onClick = {
                busy = true; msg = null
                scope.launch {
                    runCatching {
                        val providers = session.saveChannels(d.channels).providers
                        val routed = session.saveRoutes(d.chatChannel, d.chatModel, d.agentChannel, d.agentModel)
                        usage = session.loadChannels().usage
                        routed.copy(channels = providers)
                    }.onSuccess { saved = it; draft = it; msg = "已保存。" to false }
                        .onFailure { msg = ServerClient.describe(it) to true }
                    busy = false
                }
            }) { Text(if (busy) "正在保存…" else "保存设置") }
        }
        if (d != saved && !valid) Note("请先给聊天模型和任务模型各选一个已启用渠道里的模型。", Reeve.extra.warn)
    }
}

@Composable
private fun RoutePicker(title: String, s: ApiSettings, channel: String, model: String, onPick: (String, String) -> Unit) {
    var open by remember { mutableStateOf(false) }
    Box {
        val label = listOfNotNull(s.channel(channel)?.name, model.ifEmpty { null }).joinToString(" · ").ifEmpty { "未选择" }
        ListRow(title, label, onClick = { open = true })
        DropdownMenu(open, { open = false }) {
            s.channels.filter { it.enabled }.forEach { ch ->
                DropdownMenuItem(text = { Text(ch.name, color = MaterialTheme.colorScheme.primary, fontSize = 13.sp) }, onClick = {}, enabled = false)
                ch.visibleModels.forEach { m ->
                    DropdownMenuItem(
                        text = { Text(m, fontFamily = FontFamily.Monospace, fontSize = 14.sp) },
                        leadingIcon = { RadioButton(selected = ch.id == channel && m == model, onClick = null) },
                        onClick = { onPick(ch.id, m); open = false },
                    )
                }
            }
        }
    }
}

@Composable
private fun ChannelEditor(session: BotSession, start: ApiChannel, onDone: (ApiChannel) -> Unit, onRemove: () -> Unit, onCancel: () -> Unit) {
    val scope = rememberCoroutineScope()
    var ch by remember { mutableStateOf(start) }
    var key by remember { mutableStateOf(start.apiKey ?: "") }
    var newModel by remember { mutableStateOf("") }
    var note by remember { mutableStateOf<Pair<String, Boolean>?>(null) }
    var busy by remember { mutableStateOf(false) }
    var confirm by remember { mutableStateOf(false) }
    fun current() = ch.copy(apiKey = if (ch.authRequired) key.ifEmpty { null } else null)

    androidx.activity.compose.BackHandler { onCancel() }
    Scaffold(containerColor = MaterialTheme.colorScheme.surface) { pad ->
        Column(Modifier.padding(pad).fillMaxSize().verticalScroll(rememberScrollState()).padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Text(start.name, style = MaterialTheme.typography.headlineSmall)
            Text("OpenAI 兼容的接口（/v1）。API Key 加密保存在服务器上，这里不会显示。", color = MaterialTheme.colorScheme.onSurfaceVariant, fontSize = 13.sp)
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text("启用", Modifier.weight(1f)); Switch(checked = ch.enabled, onCheckedChange = { ch = ch.copy(enabled = it) })
            }
            OutlinedTextField(ch.name, { ch = ch.copy(name = it) }, label = { Text("名称") }, singleLine = true, modifier = Modifier.fillMaxWidth())
            OutlinedTextField(ch.baseUrl, { ch = ch.copy(baseUrl = it) }, label = { Text("接口地址") }, placeholder = { Text("https://api.example.com/v1") },
                singleLine = true, keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri), modifier = Modifier.fillMaxWidth())
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text("需要 API Key", Modifier.weight(1f)); Switch(checked = ch.authRequired, onCheckedChange = { ch = ch.copy(authRequired = it) })
            }
            if (ch.authRequired) {
                OutlinedTextField(key, { key = it }, label = { Text("API Key") }, placeholder = { Text(if (ch.hasApiKey) "已加密保存，留空保留" else "粘贴 API Key") },
                    singleLine = true, visualTransformation = PasswordVisualTransformation(), modifier = Modifier.fillMaxWidth())
            }
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text("可选模型 · 共 ${ch.models.size} 个，显示 ${ch.visibleModels.size} 个", Modifier.weight(1f), style = MaterialTheme.typography.labelLarge)
                TextButton(enabled = !busy && ch.baseUrl.isNotBlank(), onClick = {
                    busy = true; note = null
                    scope.launch {
                        runCatching { session.fetchModels(current()) }.onSuccess { list ->
                            val added = list.filter { it !in ch.models }
                            ch = ch.copy(models = ch.models + added, hiddenModels = if (ch.models.isEmpty()) ch.hiddenModels else ch.hiddenModels + added)
                            note = "拉到 ${list.size} 个模型" to false
                        }.onFailure { note = ServerClient.describe(it) to true }
                        busy = false
                    }
                }) { Text("拉取模型") }
            }
            ch.models.forEach { m ->
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Checkbox(checked = m !in ch.hiddenModels, onCheckedChange = { on ->
                        ch = ch.copy(hiddenModels = if (on) ch.hiddenModels - m else ch.hiddenModels + m)
                    })
                    Text(m, fontFamily = FontFamily.Monospace, fontSize = 13.sp, modifier = Modifier.weight(1f))
                }
            }
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                OutlinedTextField(newModel, { newModel = it }, label = { Text("手动添加模型 ID") }, singleLine = true, modifier = Modifier.weight(1f))
                TextButton(enabled = newModel.isNotBlank(), onClick = {
                    val m = newModel.trim()
                    if (m !in ch.models) ch = ch.copy(models = ch.models + m)
                    newModel = ""
                }) { Text("添加") }
            }
            val testModel = ch.visibleModels.firstOrNull()
            OutlinedButton(enabled = !busy && testModel != null && ch.baseUrl.isNotBlank(), onClick = {
                busy = true; note = null
                scope.launch {
                    runCatching { session.testChannel(current(), testModel!!) }
                        .onSuccess { note = "正常 · $it ms（$testModel）" to false }
                        .onFailure { note = ServerClient.describe(it) to true }
                    busy = false
                }
            }) { Text("测试连接") }
            note?.let { Text(it.first, color = if (it.second) MaterialTheme.colorScheme.error else Reeve.extra.ok, fontSize = 13.sp) }
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                TextButton(onClick = { confirm = true }) { Text("移除渠道", color = MaterialTheme.colorScheme.error) }
                Box(Modifier.weight(1f))
                OutlinedButton(onClick = onCancel) { Text("取消") }
                Button(onClick = { onDone(current()) }) { Text("完成") }
            }
            Text("点「完成」后回到渠道列表，再点「保存设置」才会生效。", fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }
    if (confirm) {
        AlertDialog(
            onDismissRequest = { confirm = false },
            title = { Text("移除渠道「${ch.name}」？") },
            text = { Text("保存设置后生效，密钥会一起删除。") },
            confirmButton = { TextButton(onClick = { confirm = false; onRemove() }) { Text("移除", color = MaterialTheme.colorScheme.error) } },
            dismissButton = { TextButton(onClick = { confirm = false }) { Text("取消") } },
        )
    }
}


