package dev.reeve.app.ui

import android.content.Intent
import android.net.Uri
import android.provider.OpenableColumns
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.Send
import androidx.compose.material.icons.filled.AddCircleOutline
import androidx.compose.material.icons.filled.ArrowDropDown
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.ExpandLess
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.Stop
import androidx.compose.material.icons.outlined.Checklist
import androidx.compose.material.icons.outlined.GppMaybe
import androidx.compose.material.icons.outlined.Terminal
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Badge
import androidx.compose.material3.BadgedBox
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.InputChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TextField
import androidx.compose.material3.TextFieldDefaults
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import dev.reeve.app.core.AgentConnections
import dev.reeve.app.core.ApiSettings
import dev.reeve.app.core.AppState
import dev.reeve.app.core.Approval
import dev.reeve.app.core.BotSession
import dev.reeve.app.core.BotTask
import dev.reeve.app.core.CommandInfo
import dev.reeve.app.core.Fmt
import dev.reeve.app.core.ItemKind
import dev.reeve.app.core.ServerClient
import dev.reeve.app.core.TaskStatus
import kotlinx.coroutines.launch

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ChatScreen(state: AppState, session: BotSession, nav: Nav) {
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    var menu by remember { mutableStateOf(false) }
    var trayOpen by remember { mutableStateOf(false) }
    var modelOpen by remember { mutableStateOf(false) }
    var detail by remember { mutableStateOf<Approval?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var confirmStop by remember { mutableStateOf(false) }
    // 命令定义是全局静态表：进页面拉一次，本地过滤，不按输入逐字请求。
    var commands by remember(session.ref) { mutableStateOf(emptyList<CommandInfo>()) }
    LaunchedEffect(session.ref) {
        commands = runCatching { session.loadCommands() }.getOrDefault(emptyList())
    }

    Scaffold(
        topBar = {
            TopAppBar(
                navigationIcon = { IconButton(onClick = { nav.pop() }) { Icon(Icons.AutoMirrored.Filled.ArrowBack, "返回") } },
                title = {
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                        BotAvatar(session.profile, 36.dp)
                        Column(Modifier.clickable { modelOpen = true }) {
                            Text(session.profile.name, style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                val model = session.info?.chatModel?.ifEmpty { null } ?: "未选模型"
                                Text(
                                    "${session.server.name} · $model", style = MaterialTheme.typography.bodySmall, maxLines = 1,
                                    overflow = TextOverflow.Ellipsis, color = MaterialTheme.colorScheme.onSurfaceVariant,
                                    modifier = Modifier.weight(1f, fill = false),
                                )
                                Icon(Icons.Filled.ArrowDropDown, "换模型", Modifier.size(18.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
                            }
                        }
                    }
                },
                actions = {
                    IconButton(onClick = { trayOpen = true }) {
                        val n = session.openTasks
                        BadgedBox(badge = { if (n > 0) Badge { Text("$n") } }) { Icon(Icons.Outlined.Checklist, "任务") }
                    }
                    Box {
                        IconButton(onClick = { menu = true }) { Icon(Icons.Filled.MoreVert, "更多") }
                        DropdownMenu(menu, { menu = false }) {
                            DropdownMenuItem(text = { Text("编辑 Bot") }, onClick = {
                                menu = false; nav.push(Screen.BotEditor(session.ref.server, session.ref))
                            })
                            DropdownMenuItem(text = { Text("换聊天模型") }, onClick = { menu = false; modelOpen = true })
                            if (session.regenerateId != null) {
                                DropdownMenuItem(text = { Text("重新生成最后一条回复") }, onClick = {
                                    menu = false; scope.launch { runCatching { session.regenerate() }.onFailure { error = ServerClient.describe(it) } }
                                })
                            }
                            if (session.openTasks > 0) {
                                DropdownMenuItem(text = { Text("停止全部任务") }, onClick = { menu = false; confirmStop = true })
                            }
                            DropdownMenuItem(text = { Text("在浏览器中打开") }, onClick = {
                                menu = false
                                runCatching { context.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(session.server.config.url))) }
                            })
                        }
                    }
                },
                colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.surface),
            )
        },
        containerColor = MaterialTheme.colorScheme.surface,
    ) { pad ->
        Column(Modifier.padding(pad).fillMaxSize().imePadding()) {
            if (!session.connected && session.ready) {
                Text(
                    "正在重新连接「${session.server.name}」，已开始的任务会继续运行",
                    style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.surfaceContainer).padding(horizontal = 16.dp, vertical = 6.dp),
                )
            }
            Timeline(session, Modifier.weight(1f)) { detail = it }
            error?.let {
                Row(Modifier.fillMaxWidth().padding(horizontal = 16.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text(it, color = MaterialTheme.colorScheme.error, fontSize = 13.sp, modifier = Modifier.weight(1f))
                    TextButton(onClick = { error = null }) { Text("知道了") }
                }
            }
            TaskStrip(session) { trayOpen = true }
            Composer(session, commands) { error = it }
        }
    }

    if (trayOpen) TraySheet(session) { trayOpen = false }
    if (modelOpen) ModelSheet(session, onEdit = {
        modelOpen = false
        nav.push(Screen.BotEditor(session.ref.server, session.ref))
    }) { modelOpen = false }
    detail?.let { a -> ApprovalSheet(session, session.approvals[a.id] ?: a) { detail = null } }
    if (confirmStop) {
        AlertDialog(
            onDismissRequest = { confirmStop = false },
            title = { Text("停止全部 ${session.openTasks} 个任务？") },
            text = { Text("包括排队和暂停的任务。") },
            confirmButton = {
                TextButton(onClick = {
                    confirmStop = false
                    scope.launch { runCatching { session.stopAll() }.onFailure { error = ServerClient.describe(it) } }
                }) { Text("全部停止", color = MaterialTheme.colorScheme.error) }
            },
            dismissButton = { TextButton(onClick = { confirmStop = false }) { Text("取消") } },
        )
    }
}

@Composable
private fun Timeline(session: BotSession, modifier: Modifier, onApprovalDetail: (Approval) -> Unit) {
    val list = rememberLazyListState()
    val count = session.items.size
    LaunchedEffect(count, session.ready, session.busy) {
        if (count > 0) list.animateScrollToItem(maxOf(0, count + (if (session.busy) 1 else 0) - 1))
    }
    LazyColumn(
        modifier, state = list,
        contentPadding = PaddingValues(horizontal = 16.dp, vertical = 12.dp),
        verticalArrangement = Arrangement.spacedBy(14.dp),
    ) {
        if (!session.ready) item { Busy(Modifier.padding(top = 80.dp)) }
        else if (session.items.isEmpty()) item { Welcome(session) }
        items(session.items, key = { it.kind.name + it.id }) { item ->
            when (item.kind) {
                ItemKind.Msg -> session.msgs[item.id]?.let { m ->
                    when (m.role) {
                        "user" -> UserBubble(m, session)
                        "notice" -> NoticeRow(m)
                        else -> AssistantMessage(session, m)
                    }
                }
                ItemKind.Task -> session.tasks[item.id]?.let { t ->
                    Box(Modifier.padding(start = 40.dp)) { TaskCard(session, t, session.queuePositions[t.id]) {} }
                }
                ItemKind.Approval -> session.approvals[item.id]?.let { a ->
                    Box(Modifier.padding(start = 40.dp)) { ApprovalCard(session, a) { onApprovalDetail(a) } }
                }
            }
        }
        // 只有重放结束（replay.done）才显示流式回复，避免把补发的半截状态当成「已完成」。
        if (session.busy && session.ready) item(key = "live") { LiveReply(session) }
    }
}

@Composable
private fun Welcome(session: BotSession) {
    Column(Modifier.fillMaxWidth().padding(top = 60.dp), horizontalAlignment = Alignment.CenterHorizontally) {
        BotAvatar(session.profile, 72.dp)
        Spacer(Modifier.size(16.dp))
        Text("你好，我是 ${session.profile.name}", style = MaterialTheme.typography.headlineSmall)
        Spacer(Modifier.size(8.dp))
        Text(
            session.profile.instructions.ifBlank { "能在这台服务器的工作区里写代码、跑命令、管服务器。复杂的事我会拆成任务在后台做。" },
            style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.width(300.dp), maxLines = 4, overflow = TextOverflow.Ellipsis,
        )
    }
}

/** 输入框上方的任务条：有任务在跑、排队或暂停时一直显示 */
@Composable
private fun TaskStrip(session: BotSession, onOpen: () -> Unit) {
    val running = session.running
    val open = session.tasks.values.filter { it.isOpen }
    if (open.isEmpty()) return
    val awaiting = running.firstOrNull { it.awaiting > 0 }
    val lead = awaiting ?: running.maxByOrNull { it.lastEvent }
    val ex = Reeve.extra
    val bg = if (awaiting != null) ex.attContainer else MaterialTheme.colorScheme.surfaceContainerHigh
    val fg = if (awaiting != null) ex.onAttContainer else MaterialTheme.colorScheme.onSurface
    Row(
        Modifier.padding(horizontal = 12.dp, vertical = 4.dp).fillMaxWidth().heightIn(min = 40.dp)
            .background(bg, RoundedCornerShape(20.dp)).clickable(onClick = onOpen).padding(horizontal = 14.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        when {
            awaiting != null -> Icon(Icons.Outlined.GppMaybe, null, Modifier.size(18.dp), tint = fg)
            lead != null -> CircularProgressIndicator(Modifier.size(16.dp), strokeWidth = 2.dp)
            else -> StatusDot(MaterialTheme.colorScheme.onSurfaceVariant)
        }
        val text = when {
            lead != null -> lead.title + " · " + Fmt.nowDoing(lead)
            session.queued.isNotEmpty() -> "${session.queued.size} 个任务排队，马上开始"
            else -> "${session.paused.size} 个任务已暂停或待办"
        }
        Text(text, color = fg, fontSize = 13.sp, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
        if (open.size > 1) Text("还有 ${open.size - 1} 个", color = fg.copy(alpha = 0.7f), fontSize = 12.sp)
        Icon(Icons.Filled.ExpandLess, null, Modifier.size(18.dp), tint = fg)
    }
}

@Composable
private fun Composer(session: BotSession, commands: List<CommandInfo>, onError: (String) -> Unit) {
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    var sending by remember { mutableStateOf(false) }
    var commandSheet by remember { mutableStateOf(false) }
    val pick = rememberLauncherForActivityResult(ActivityResultContracts.GetMultipleContents()) { uris ->
        for (uri in uris) {
            val cr = context.contentResolver
            val name = cr.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use { c ->
                if (c.moveToFirst()) c.getString(0) else null
            } ?: "file"
            val mime = cr.getType(uri) ?: "application/octet-stream"
            val bytes = runCatching { cr.openInputStream(uri)?.use { it.readBytes() } }.getOrNull()
            if (bytes == null) onError("读不了「$name」") else if (bytes.size > 25 shl 20) onError("「$name」超过 25 MB") else session.attach(name, mime, bytes)
        }
    }
    val uploading = session.uploads.any { it.uploading }
    val canSend = session.connected && session.ready && !sending && !uploading &&
        (session.draft.isNotBlank() || session.uploads.any { it.attachment != null })
    val showStop = session.busy && session.draft.isEmpty() && session.uploads.isEmpty()
    val query = commandQuery(session.draft)

    // 斜线命令直接生效，不走模型：结果由 command.result 事件渲染成 notice 行，不阻塞输入。
    fun runLine(line: String) {
        val l = line.trim()
        if (l.length < 2 || !l.startsWith("/")) return
        val saved = session.draft
        session.draft = ""
        scope.launch {
            runCatching { session.runCommand(l) }.onFailure {
                if (session.draft.isEmpty()) session.draft = saved
                onError(ServerClient.describe(it))
            }
        }
    }

    fun send() {
        if (!canSend) return
        if (session.draft.trim().startsWith("/")) {
            runLine(session.draft)
            return
        }
        val text = session.draft.trim()
        val files = session.uploads.mapNotNull { it.attachment }
        val saved = session.draft
        val savedUploads = session.uploads.toList()
        session.draft = ""
        session.uploads.clear()
        sending = true
        scope.launch {
            runCatching { session.send(text, files) }.onFailure {
                if (session.draft.isEmpty()) session.draft = saved
                if (session.uploads.isEmpty()) session.uploads.addAll(savedUploads)
                onError(ServerClient.describe(it))
            }
            sending = false
        }
    }

    Column(Modifier.fillMaxWidth().navigationBarsPadding().padding(horizontal = 8.dp, vertical = 6.dp)) {
        if (session.uploads.isNotEmpty()) {
            LazyRow(horizontalArrangement = Arrangement.spacedBy(6.dp), contentPadding = PaddingValues(horizontal = 8.dp)) {
                items(session.uploads, key = { it.id }) { u ->
                    InputChip(
                        selected = false, onClick = {},
                        label = { Text(u.error ?: u.name, maxLines = 1, overflow = TextOverflow.Ellipsis, color = if (u.error != null) MaterialTheme.colorScheme.error else Color.Unspecified) },
                        leadingIcon = { if (u.uploading) CircularProgressIndicator(Modifier.size(14.dp), strokeWidth = 2.dp) },
                        trailingIcon = {
                            Icon(Icons.Filled.Close, "移除", Modifier.size(16.dp).clickable { session.uploads.removeAll { it.id == u.id } })
                        },
                    )
                }
            }
        }
        if (query != null) {
            CommandPanel(
                commands = commands, query = query,
                onComplete = { session.draft = it },
                modifier = Modifier.padding(bottom = 4.dp),
            )
        }
        Row(verticalAlignment = Alignment.Bottom) {
            IconButton(onClick = { commandSheet = true }) { Icon(Icons.Outlined.Terminal, "命令", tint = MaterialTheme.colorScheme.onSurfaceVariant) }
            IconButton(onClick = { pick.launch("*/*") }) { Icon(Icons.Filled.AddCircleOutline, "添加附件", tint = MaterialTheme.colorScheme.onSurfaceVariant) }
            TextField(
                value = session.draft, onValueChange = { session.draft = it },
                placeholder = { Text(if (session.connected) "消息" else "正在连接…") },
                maxLines = 6,
                shape = RoundedCornerShape(24.dp),
                keyboardOptions = KeyboardOptions(imeAction = if (query != null) ImeAction.Send else ImeAction.Default),
                keyboardActions = KeyboardActions(onSend = { runLine(session.draft) }),
                colors = TextFieldDefaults.colors(
                    focusedContainerColor = MaterialTheme.colorScheme.surfaceContainerHigh,
                    unfocusedContainerColor = MaterialTheme.colorScheme.surfaceContainerHigh,
                    focusedIndicatorColor = Color.Transparent, unfocusedIndicatorColor = Color.Transparent,
                ),
                modifier = Modifier.weight(1f),
            )
            Spacer(Modifier.width(6.dp))
            Box(
                Modifier.size(48.dp).background(
                    if (canSend || showStop) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.surfaceContainerHighest, CircleShape,
                ).clickable(enabled = canSend || showStop) {
                    if (showStop) scope.launch { runCatching { session.stopReply() } } else send()
                },
                contentAlignment = Alignment.Center,
            ) {
                if (sending) CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp, color = MaterialTheme.colorScheme.onPrimary)
                else Icon(
                    if (showStop) Icons.Filled.Stop else Icons.AutoMirrored.Filled.Send, if (showStop) "停止回复" else "发送",
                    tint = if (canSend || showStop) MaterialTheme.colorScheme.onPrimary else MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        }
    }
    if (commandSheet) {
        CommandSheet(
            commands = commands, onComplete = { session.draft = it }, onClose = { commandSheet = false },
        )
    }
}

// ---------- 底部弹出的面板 ----------

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ApprovalSheet(session: BotSession, a: Approval, onClose: () -> Unit) {
    val scope = rememberCoroutineScope()
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    val ex = Reeve.extra
    fun decide(d: String) {
        busy = true; error = null
        scope.launch {
            runCatching { session.approve(a.id, d) }
                .onSuccess { onClose() }.onFailure { error = ServerClient.describe(it) }
            busy = false
        }
    }
    ModalBottomSheet(onDismissRequest = onClose) {
        Column(Modifier.padding(horizontal = 24.dp).padding(bottom = 24.dp), verticalArrangement = Arrangement.spacedBy(14.dp)) {
            Row(horizontalArrangement = Arrangement.spacedBy(12.dp), verticalAlignment = Alignment.CenterVertically) {
                Icon(Icons.Outlined.GppMaybe, null, tint = ex.att)
                Text(approvalQuestion(a), style = MaterialTheme.typography.titleLarge)
            }
            val sub = listOfNotNull(
                session.tasks[a.taskId]?.title?.let { "「$it」" },
                toolLabels[a.toolName]?.let { "$it（${a.toolName}）" } ?: a.toolName,
                riskLabels[a.risk] ?: a.risk,
            ).joinToString(" · ")
            Text(sub, color = MaterialTheme.colorScheme.onSurfaceVariant)
            if (a.reason.isNotEmpty()) Text(a.reason)
            CodeBox(approvalBody(a))
            error?.let { Text(it, color = MaterialTheme.colorScheme.error, fontSize = 13.sp) }
            if (a.status == "pending") {
                Button(onClick = { decide("once") }, enabled = !busy, modifier = Modifier.fillMaxWidth()) { Text("允许一次") }
                if (!a.fromChat) FilledTonalButton(onClick = { decide("session") }, enabled = !busy, modifier = Modifier.fillMaxWidth()) { Text("本任务内都允许") }
                OutlinedButton(onClick = { decide("deny") }, enabled = !busy, modifier = Modifier.fillMaxWidth()) { Text("拒绝", color = MaterialTheme.colorScheme.error) }
                Text("只放行这一条 · 10 分钟内不处理会自动拒绝", fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun TraySheet(session: BotSession, onClose: () -> Unit) {
    val scope = rememberCoroutineScope()
    val now = rememberNow(true)
    val pos = session.queuePositions
    val done = session.tasks.values.filter { it.status.terminal && it.ended != null && now - (it.ended ?: 0.0) < 30 * 60_000 }
        .sortedByDescending { it.ended }.take(3)
    val groups = listOf(
        "等你确认" to session.running.filter { it.awaiting > 0 },
        "运行中" to session.running.filter { it.awaiting == 0 },
        "排队中" to session.queued,
        "已暂停 / 待办" to session.paused,
        "最近完成" to done,
    ).filter { it.second.isNotEmpty() }
    ModalBottomSheet(onDismissRequest = onClose) {
        Row(Modifier.fillMaxWidth().padding(horizontal = 24.dp), verticalAlignment = Alignment.CenterVertically) {
            Text("任务", style = MaterialTheme.typography.titleLarge, modifier = Modifier.weight(1f))
            if (session.openTasks > 0) TextButton(onClick = { scope.launch { runCatching { session.stopAll() } } }) {
                Text("全部停止", color = MaterialTheme.colorScheme.error)
            }
        }
        LazyColumn(Modifier.fillMaxWidth().heightIn(max = 520.dp), contentPadding = PaddingValues(bottom = 24.dp)) {
            if (groups.isEmpty()) item {
                Text("现在没有任务", Modifier.padding(24.dp), color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
            for ((label, list) in groups) {
                item(key = "l-$label") { SectionLabel(label) }
                items(list, key = { "t-" + it.id }) { t -> TrayRow(session, t, pos[t.id], now) }
            }
        }
    }
}

@Composable
private fun TrayRow(session: BotSession, t: BotTask, pos: Int?, now: Double) {
    Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 6.dp)) {
        Text(t.title, style = MaterialTheme.typography.bodyLarge, maxLines = 1, overflow = TextOverflow.Ellipsis)
        Text(Fmt.statusLine(t, now, pos), style = MaterialTheme.typography.bodySmall, color = if (t.awaiting > 0) Reeve.extra.att else MaterialTheme.colorScheme.onSurfaceVariant)
        if (t.status != TaskStatus.completed) TaskActions(session, t, pos)
    }
}

/** 换聊天模型：只对这个 Bot 生效 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun ModelSheet(session: BotSession, onEdit: () -> Unit, onClose: () -> Unit) {
    val scope = rememberCoroutineScope()
    var settings by remember { mutableStateOf<ApiSettings?>(null) }
    var conn by remember { mutableStateOf<AgentConnections?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var saving by remember { mutableStateOf(false) }
    LaunchedEffect(session.ref) {
        runCatching { session.loadSettings() }.onSuccess { settings = it }.onFailure { error = ServerClient.describe(it) }
        conn = runCatching { session.loadConnections() }.getOrNull()
    }
    ModalBottomSheet(onDismissRequest = onClose) {
        Column(Modifier.padding(bottom = 24.dp)) {
            Text("${session.profile.name} 的聊天模型", style = MaterialTheme.typography.titleLarge, modifier = Modifier.padding(horizontal = 24.dp))
            Text("只对这个 Bot 生效。任务模型在 Bot 设置里改。", style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(horizontal = 24.dp, vertical = 4.dp))
            val s = settings
            when {
                error != null -> Note(error!!, MaterialTheme.colorScheme.error)
                s == null -> Busy()
                s.channels.none { it.enabled } -> Note("这个 Bot 还没有可用的 API 渠道。到「设置 › 服务器 › API 渠道」里添加。")
                else -> LazyColumn(Modifier.heightIn(max = 440.dp)) {
                    for (ch in s.channels.filter { it.enabled }) {
                        item(key = "c-" + ch.id) { SectionLabel(ch.name) }
                        items(ch.visibleModels, key = { ch.id + "/" + it }) { m ->
                            val selected = s.chatChannel == ch.id && s.chatModel == m
                            Row(
                                Modifier.fillMaxWidth().clickable(enabled = !saving) {
                                    saving = true
                                    scope.launch {
                                        runCatching { session.setChatModel(ch.id, m) }
                                            .onSuccess { onClose() }.onFailure { error = ServerClient.describe(it) }
                                        saving = false
                                    }
                                }.padding(horizontal = 16.dp, vertical = 4.dp),
                                verticalAlignment = Alignment.CenterVertically,
                            ) {
                                RadioButton(selected = selected, onClick = null)
                                Spacer(Modifier.width(12.dp))
                                Text(m, fontFamily = FontFamily.Monospace, fontSize = 14.sp)
                            }
                        }
                    }
                }
            }
            conn?.takeIf { it.cli.isNotEmpty() }?.let { c ->
                Note("后台任务由 ${c.clis.firstOrNull { it.id == c.cli }?.name ?: c.cli} 执行")
            }
            HorizontalDivider(Modifier.padding(vertical = 8.dp))
            TextButton(onClick = onEdit, modifier = Modifier.padding(horizontal = 12.dp)) { Text("编辑 Bot 和任务模型") }
        }
    }
}
