package dev.reeve.app.ui

import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.ExpandLess
import androidx.compose.material.icons.filled.ExpandMore
import androidx.compose.material.icons.outlined.Block
import androidx.compose.material.icons.outlined.Description
import androidx.compose.material.icons.outlined.Edit
import androidx.compose.material.icons.outlined.Folder
import androidx.compose.material.icons.outlined.GppMaybe
import androidx.compose.material.icons.outlined.HourglassEmpty
import androidx.compose.material.icons.outlined.InsertDriveFile
import androidx.compose.material.icons.outlined.Key
import androidx.compose.material.icons.outlined.Pause
import androidx.compose.material.icons.outlined.Search
import androidx.compose.material.icons.outlined.Terminal
import androidx.compose.material3.AssistChip
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.FilterChip
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableDoubleStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import dev.reeve.app.core.Approval
import dev.reeve.app.core.Attachment
import dev.reeve.app.core.BotSession
import dev.reeve.app.core.BotTask
import dev.reeve.app.core.Card
import dev.reeve.app.core.Fmt
import dev.reeve.app.core.Msg
import dev.reeve.app.core.SecretReq
import dev.reeve.app.core.ServerClient
import dev.reeve.app.core.Stamp
import dev.reeve.app.core.Step
import dev.reeve.app.core.TaskStatus
import dev.reeve.app.core.str
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/** 每秒走一次的“现在”，运行中的任务计时用 */
@Composable
fun rememberNow(live: Boolean): Double {
    var now by remember { mutableDoubleStateOf(Stamp.now()) }
    LaunchedEffect(live) {
        while (live) { now = Stamp.now(); delay(1000) }
    }
    return now
}

// ---------- 消息 ----------

@Composable
fun UserBubble(msg: Msg, session: BotSession) {
    Column(Modifier.fillMaxWidth(), horizontalAlignment = Alignment.End, verticalArrangement = Arrangement.spacedBy(4.dp)) {
        if (msg.files.isNotEmpty()) Files(msg.files)
        if (msg.text.isNotEmpty()) {
            Box(
                Modifier.widthIn(max = 300.dp)
                    .background(MaterialTheme.colorScheme.primary, RoundedCornerShape(20.dp, 20.dp, 4.dp, 20.dp))
                    .padding(horizontal = 14.dp, vertical = 10.dp),
            ) {
                Text(msg.text, color = MaterialTheme.colorScheme.onPrimary, fontSize = 15.sp, lineHeight = 22.sp)
            }
        }
        Reactions(msg)
    }
}

/** 斜线命令的结果（command.result）：居中、等宽小字，失败用警告色；没有操作按钮。 */
@Composable
fun NoticeRow(msg: Msg) {
    val color = if (msg.ok) MaterialTheme.colorScheme.onSurfaceVariant else Reeve.extra.warn
    Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.Center) {
        Text(
            msg.text, fontFamily = FontFamily.Monospace, fontSize = 12.sp, lineHeight = 19.8.sp,
            color = color,
            modifier = Modifier.widthIn(max = 320.dp)
                .background(MaterialTheme.colorScheme.surfaceContainerLow, Shapes12)
                .semantics {
                    contentDescription = msg.text
                    liveRegion = if (msg.ok) LiveRegionMode.Polite else LiveRegionMode.Assertive
                }
                .padding(horizontal = 12.dp, vertical = 6.dp),
        )
    }
}

/** 流式区：thinking 与 streaming 两块；`replay.done` 之前由外层挡住，不看半截状态。 */
@Composable
fun LiveReply(session: BotSession) {
    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        if (session.thinking.isNotEmpty()) LiveThinking(session.thinking)
        AssistantRow(session, session.streaming, streaming = true) {
            if (session.streaming.isEmpty() && session.toolLabel.isNotEmpty()) {
                Text(Fmt.liveToolLabel(session.toolLabel), fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
    }
}

@Composable
private fun LiveThinking(text: String) {
    var open by remember { mutableStateOf(false) }
    Column(Modifier.padding(start = 40.dp), verticalArrangement = Arrangement.spacedBy(2.dp)) {
        Text(
            "正在思考 " + if (open) "▴" else "▾",
            style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.clickable { open = !open },
        )
        if (open) {
            Text(text, fontStyle = FontStyle.Italic, fontSize = 13.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }
}

/** 助理的回复：一句一个气泡，头像贴着最后一个气泡 */
@Composable
fun AssistantRow(session: BotSession, text: String, streaming: Boolean = false, extras: @Composable () -> Unit = {}) {
    val pieces = remember(text) { bubblePieces(text) }
    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        Row(verticalAlignment = Alignment.Bottom, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            BotAvatar(session.profile, 32.dp)
            Column(verticalArrangement = Arrangement.spacedBy(2.dp), modifier = Modifier.weight(1f)) {
                if (pieces.isEmpty() && streaming) Bubble(true) { TypingDots() }
                pieces.forEachIndexed { i, p ->
                    val last = i == pieces.lastIndex
                    if (p is MdBlock.Code) CodeBox(p.code)
                    else Bubble(last) { MdBlockView(p, caret = streaming && last) }
                }
            }
        }
        Column(Modifier.padding(start = 40.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) { extras() }
    }
}

private fun bubblePieces(text: String): List<MdBlock> = Markdown.parse(text).flatMap { b ->
    if (b is MdBlock.Para) Fmt.sentences(b.text).ifEmpty { listOf(b.text) }.map { MdBlock.Para(it) } else listOf(b)
}

@Composable
private fun Bubble(last: Boolean, content: @Composable () -> Unit) {
    Box(
        Modifier.widthIn(max = 290.dp)
            .background(MaterialTheme.colorScheme.surfaceContainerHigh, RoundedCornerShape(20.dp, 20.dp, 20.dp, if (last) 4.dp else 20.dp))
            .padding(horizontal = 14.dp, vertical = 10.dp),
    ) { content() }
}

@Composable
fun TypingDots() {
    val now = rememberNow(true)
    val phase = ((now / 400).toLong() % 3).toInt()
    Row(horizontalArrangement = Arrangement.spacedBy(5.dp), modifier = Modifier.padding(vertical = 6.dp)) {
        repeat(3) { i ->
            Box(Modifier.size(7.dp).background(MaterialTheme.colorScheme.onSurfaceVariant.copy(alpha = if (i == phase) 0.9f else 0.35f), RoundedCornerShape(50)))
        }
    }
}

@Composable
fun AssistantMessage(session: BotSession, msg: Msg) {
    AssistantRow(session, msg.text.ifEmpty { if (msg.card == null && msg.secret == null && msg.files.isEmpty()) "…" else "" }) {
        if (!msg.thinking.isNullOrEmpty()) Thinking(msg.thinking, msg.thinkingMs)
        if (msg.files.isNotEmpty()) Files(msg.files)
        msg.card?.let { OptionCard(session, it) }
        msg.secret?.let { SecretCard(session, it) }
        Reactions(msg)
    }
}

@Composable
private fun Thinking(text: String, ms: Double?) {
    var open by remember { mutableStateOf(false) }
    Column(Modifier.animateContentSize()) {
        Text(
            "已思考 ${maxOf(1, ((ms ?: 0.0) / 1000).toInt())} 秒 " + if (open) "▴" else "▾",
            style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.clickable { open = !open },
        )
        if (open) Text(text, fontStyle = FontStyle.Italic, fontSize = 13.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

@Composable
private fun Reactions(msg: Msg) {
    if (msg.reactions.isEmpty()) return
    Row(horizontalArrangement = Arrangement.spacedBy(4.dp)) {
        msg.reactions.forEach { r ->
            Text(
                r.emoji, fontSize = 14.sp,
                modifier = Modifier.background(MaterialTheme.colorScheme.surfaceContainerHigh, RoundedCornerShape(50)).padding(horizontal = 8.dp, vertical = 2.dp),
            )
        }
    }
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun Files(files: List<Attachment>) {
    FlowRow(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
        files.forEach { f ->
            AssistChip(
                onClick = {}, label = { Text(f.name, maxLines = 1, overflow = TextOverflow.Ellipsis) },
                leadingIcon = { Icon(if (f.isImage) Icons.Outlined.Description else Icons.Outlined.InsertDriveFile, null, Modifier.size(18.dp)) },
            )
        }
    }
}

// ---------- 任务卡片 ----------

@Composable
fun TaskCard(session: BotSession, task: BotTask, pos: Int?, onApprove: () -> Unit) {
    var userOpen by remember { mutableStateOf<Boolean?>(null) }
    val open = userOpen ?: (task.status == TaskStatus.running)
    val now = rememberNow(task.isLive)
    val ex = Reeve.extra
    Column(
        Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.surfaceContainerHigh, Shapes16)
            .let { if (task.awaiting > 0) it.border(1.dp, ex.att.copy(alpha = 0.5f), Shapes16) else it }
            .animateContentSize(),
    ) {
        Row(
            Modifier.fillMaxWidth().clickable { userOpen = !open }.padding(horizontal = 16.dp, vertical = 12.dp),
            verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            StatusIcon(task)
            Column(Modifier.weight(1f)) {
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    Text(
                        task.title, style = MaterialTheme.typography.titleSmall, maxLines = 1, overflow = TextOverflow.Ellipsis,
                        modifier = Modifier.weight(1f, fill = false),
                    )
                    ExecutorBadge(task)
                }
                Text(
                    Fmt.statusLine(task, now, pos), style = MaterialTheme.typography.bodySmall, maxLines = 1, overflow = TextOverflow.Ellipsis,
                    color = if (task.awaiting > 0) ex.att else MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            Icon(if (open) Icons.Filled.ExpandLess else Icons.Filled.ExpandMore, null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
        }
        if (open) {
            Column(Modifier.padding(bottom = 8.dp)) {
                if (task.steps.isEmpty()) {
                    Text(
                        if (task.status == TaskStatus.queued) "等前面的任务做完…" else "正在看要做什么…",
                        style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.padding(horizontal = 16.dp, vertical = 6.dp),
                    )
                }
                task.steps.forEach { StepRow(it, now) }
                if (task.status == TaskStatus.running && task.text.isNotEmpty()) {
                    Text(task.text.takeLast(300) + " ▍", fontSize = 13.sp, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(horizontal = 16.dp, vertical = 6.dp))
                }
                if (!task.isLive && !task.error.isNullOrEmpty()) {
                    Text(
                        task.error, fontSize = 13.sp, color = MaterialTheme.colorScheme.error,
                        modifier = Modifier.padding(horizontal = 16.dp, vertical = 6.dp),
                    )
                }
                if (task.status == TaskStatus.completed) {
                    val result = (task.summary ?: task.output ?: "").trim()
                    if (result.isNotEmpty()) {
                        Text(result.replace(Regex("[*`#]"), ""), fontSize = 13.sp, maxLines = 4, overflow = TextOverflow.Ellipsis,
                            color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(horizontal = 16.dp, vertical = 6.dp))
                    }
                }
                TaskActions(session, task, pos, Modifier.padding(horizontal = 8.dp))
            }
        }
    }
}

/** 外部执行器的小徽标（builtin 不显示）：点一下看说明。 */
@Composable
fun ExecutorBadge(task: BotTask) {
    if (task.executor.isEmpty() || task.executor == "builtin") return
    val ex = Reeve.extra
    var open by remember { mutableStateOf(false) }
    Box {
        Text(
            task.executor, fontFamily = FontFamily.Monospace, fontSize = 10.5.sp, color = ex.warn,
            modifier = Modifier
                .semantics { contentDescription = "${task.executor}，由外部 CLI 执行，内部命令不受 reeve 审批" }
                .background(ex.warn.copy(alpha = 0.12f), RoundedCornerShape(50))
                .clickable { open = true }
                .padding(horizontal = 4.dp, vertical = 1.dp),
        )
        DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
            Text("由外部 CLI 执行，内部命令不受 reeve 审批", fontSize = 13.sp, modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp))
        }
    }
}

@Composable
private fun StatusIcon(task: BotTask) {
    val ex = Reeve.extra
    Box(Modifier.size(22.dp), contentAlignment = Alignment.Center) {
        when {
            task.awaiting > 0 -> Icon(Icons.Outlined.GppMaybe, null, tint = ex.att)
            task.status == TaskStatus.running -> CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.5.dp)
            task.status == TaskStatus.completed -> Icon(Icons.Filled.Check, null, tint = ex.ok)
            task.status == TaskStatus.failed -> Icon(Icons.Filled.Close, null, tint = MaterialTheme.colorScheme.error)
            task.status == TaskStatus.paused -> Icon(Icons.Outlined.Pause, null, tint = ex.warn)
            task.status == TaskStatus.queued -> Icon(Icons.Outlined.HourglassEmpty, null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
            else -> Icon(Icons.Outlined.Block, null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }
}

@Composable
fun TaskActions(session: BotSession, task: BotTask, pos: Int?, modifier: Modifier = Modifier) {
    val scope = rememberCoroutineScope()
    fun act(a: String) { scope.launch { runCatching { session.taskAction(task.id, a) } } }
    Row(modifier, horizontalArrangement = Arrangement.spacedBy(4.dp)) {
        when (task.status) {
            TaskStatus.running -> {
                if (!task.pausing) TextButton(onClick = { act("pause") }) { Text("暂停") }
                TextButton(onClick = { act("cancel") }) { Text("停止", color = MaterialTheme.colorScheme.error) }
            }
            // 外部执行器重启后原进程已经不在了：只能按原要求重新派一次，不能「继续」。
            TaskStatus.paused -> if (task.stopReason == "interrupted") {
                Button(onClick = { act("retry") }) { Text("重新执行") }
                TextButton(onClick = { act("cancel") }) { Text("停止", color = MaterialTheme.colorScheme.error) }
            } else {
                TextButton(onClick = { act("resume") }) { Text(if (task.stopReason == "held") "开始" else "继续") }
                TextButton(onClick = { act("cancel") }) { Text("停止", color = MaterialTheme.colorScheme.error) }
            }
            TaskStatus.queued -> {
                if ((pos ?: 0) > 1) TextButton(onClick = { act("top") }) { Text("下一个做") }
                TextButton(onClick = { act("pause") }) { Text("挂起") }
                TextButton(onClick = { act("cancel") }) { Text("停止", color = MaterialTheme.colorScheme.error) }
            }
            TaskStatus.failed, TaskStatus.cancelled -> TextButton(onClick = { act("retry") }) { Text("重试") }
            TaskStatus.completed -> {}
        }
    }
}

@Composable
private fun StepRow(step: Step, now: Double) {
    var open by remember { mutableStateOf(false) }
    val (verb, target) = Fmt.describe(step.tool, step.args)
    val icon = when (step.tool) {
        "read_file" -> Icons.Outlined.Description
        "write_file", "edit_file" -> Icons.Outlined.Edit
        "list_dir" -> Icons.Outlined.Folder
        "grep" -> Icons.Outlined.Search
        else -> Icons.Outlined.Terminal
    }
    Column(Modifier.fillMaxWidth().clickable(enabled = step.output != null) { open = !open }.padding(horizontal = 16.dp, vertical = 6.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
            Icon(icon, null, Modifier.size(18.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
            Text(verb, fontSize = 13.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
            Text(target, fontSize = 12.5.sp, fontFamily = FontFamily.Monospace, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
            when {
                !step.done -> {
                    if (now - step.at > 3000) Text("${((now - step.at) / 1000).toInt()}s", fontSize = 11.sp, color = MaterialTheme.colorScheme.primary)
                    CircularProgressIndicator(Modifier.size(14.dp), strokeWidth = 2.dp)
                }
                step.denied -> Text("未批准", fontSize = 11.sp, color = Reeve.extra.warn)
                step.error -> Text("出错", fontSize = 11.sp, color = Reeve.extra.warn)
                else -> Icon(Icons.Filled.Check, null, Modifier.size(16.dp), tint = Reeve.extra.ok)
            }
        }
        if (open && step.output != null) {
            Spacer(Modifier.size(6.dp))
            CodeBox(step.output.take(4000))
        }
    }
}

// ---------- 审批卡片 ----------

val toolLabels = mapOf(
    "shell" to "执行命令", "run_agent_cli" to "启动外部 Agent", "write_file" to "写入文件", "edit_file" to "修改文件",
    "read_file" to "读取文件", "list_dir" to "查看目录", "grep" to "搜索", "delete_secret" to "删除密钥",
)
val riskLabels = mapOf("danger" to "高风险", "exec" to "执行命令", "write" to "写入文件", "read" to "只读")

fun approvalQuestion(a: Approval) = when (a.toolName) {
    "shell" -> "允许执行这条命令吗？"
    "run_agent_cli" -> "允许启动外部 Agent 吗？"
    "write_file" -> "允许写入这个文件吗？"
    "edit_file" -> "允许修改这个文件吗？"
    "delete_secret" -> "允许删除这个密钥吗？"
    else -> "允许这个操作吗？"
}

fun approvalTarget(a: Approval): String {
    val g = a.args
    return when (a.toolName) {
        "shell" -> "$ " + g.str("command")
        "run_agent_cli" -> g.str("cli") + "\n" + g.str("task")
        "write_file", "edit_file" -> g.str("path")
        "delete_secret" -> g.str("name")
        else -> g.toString(2)
    }
}

/**
 * 审批正文：派活的审批先写「执行器：X · 标题」，再给正文预览（完整正文留给任务记录）；
 * 没有预览时显示美化后的 args。执行器与标题都缺才只显示预览。
 */
fun approvalBody(a: Approval): String {
    val args = a.args
    val preview = args.str("preview")
    if (preview.isEmpty()) return args.toString(2)
    val executor = args.str("executor")
    val title = args.str("title")
    val head = listOfNotNull(
        executor.ifEmpty { null }?.let { "执行器：$it" },
        title.ifEmpty { null },
    ).joinToString(" · ")
    return if (head.isEmpty()) preview else "$head\n$preview"
}

@Composable
fun ApprovalCard(session: BotSession, a: Approval, onDetails: () -> Unit) {
    val ex = Reeve.extra
    if (a.status != "pending") {
        val (text, color) = when (a.status) {
            "allow" -> (if (a.scope == "session") "已允许，本任务内不再询问" else "已允许一次") to ex.ok
            "deny" -> "已拒绝" to MaterialTheme.colorScheme.error
            else -> "已失效" to MaterialTheme.colorScheme.onSurfaceVariant
        }
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Icon(if (a.status == "allow") Icons.Filled.Check else Icons.Filled.Close, null, Modifier.size(16.dp), tint = color)
            Text(text, fontSize = 13.sp, color = color)
            Text(approvalTarget(a).lines().first(), fontSize = 12.sp, fontFamily = FontFamily.Monospace, maxLines = 1, overflow = TextOverflow.Ellipsis,
                color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
        return
    }
    val scope = rememberCoroutineScope()
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    fun decide(d: String) {
        busy = true; error = null
        scope.launch {
            runCatching { session.approve(a.id, d) }.onFailure { error = ServerClient.describe(it) }
            busy = false
        }
    }
    Column(
        Modifier.fillMaxWidth().background(ex.attContainer, Shapes16).padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            Icon(Icons.Outlined.GppMaybe, null, tint = ex.onAttContainer)
            Column(Modifier.weight(1f)) {
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text("需要你的批准", color = ex.onAttContainer, style = MaterialTheme.typography.titleSmall)
                    Text(
                        riskLabels[a.risk] ?: a.risk, fontSize = 11.sp, color = ex.onAttContainer,
                        modifier = Modifier.background(MaterialTheme.colorScheme.surface.copy(alpha = 0.6f), RoundedCornerShape(50))
                            .padding(horizontal = 6.dp, vertical = 1.dp),
                    )
                }
                val sub = listOfNotNull(
                    session.tasks[a.taskId]?.title?.let { "「$it」" },
                    toolLabels[a.toolName]?.let { "$it（${a.toolName}）" } ?: a.toolName,
                ).joinToString(" · ")
                if (sub.isNotEmpty()) Text(sub, color = ex.onAttContainer.copy(alpha = 0.8f), fontSize = 13.sp)
                if (a.reason.isNotEmpty()) Text(a.reason, color = ex.onAttContainer.copy(alpha = 0.8f), fontSize = 13.sp)
            }
        }
        Text(
            approvalBody(a), fontFamily = FontFamily.Monospace, fontSize = 12.5.sp, maxLines = 12, overflow = TextOverflow.Ellipsis,
            color = MaterialTheme.colorScheme.onSurface,
            modifier = Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.surface, Shapes12)
                .clickable(onClick = onDetails).padding(12.dp),
        )
        error?.let { Text(it, color = MaterialTheme.colorScheme.error, fontSize = 13.sp) }
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.End)) {
            OutlinedButton(onClick = { decide("deny") }, enabled = !busy) { Text("拒绝", color = MaterialTheme.colorScheme.error) }
            if (!a.fromChat) FilledTonalButton(onClick = { decide("session") }, enabled = !busy) { Text("本任务内都允许") }
            Button(onClick = { decide("once") }, enabled = !busy) { Text("允许一次") }
        }
    }
}

// ---------- 选项卡片、密钥卡片 ----------

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun OptionCard(session: BotSession, card: Card) {
    val scope = rememberCoroutineScope()
    val pending = card.status == "pending"
    var free by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    fun answer(text: String) {
        if (text.isBlank() || busy) return
        busy = true
        scope.launch {
            runCatching { session.answerCard(card.id, text.trim()) }
            busy = false
        }
    }
    Column(
        Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.surfaceContainerHigh, Shapes16).padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        Text(card.question, style = MaterialTheme.typography.titleSmall)
        FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            card.options.forEach { o ->
                FilterChip(
                    selected = card.answer == o, enabled = pending && !busy,
                    onClick = { answer(o) },
                    label = { Text(o) },
                )
            }
        }
        if (pending) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                OutlinedTextField(
                    value = free, onValueChange = { free = it }, label = { Text("其他回答") }, singleLine = true,
                    enabled = !busy, modifier = Modifier.weight(1f),
                )
                Button(onClick = { answer(free) }, enabled = free.isNotBlank() && !busy) { Text("回答") }
            }
            TextButton(onClick = { scope.launch { runCatching { session.answerCard(card.id, dismiss = true) } } }) { Text("都不选") }
        }
        if (card.status == "dismissed") Text("已关闭，未选择", fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
        // 自由输入的回答没有对应的选项 chip，回答后要把原文回显出来。
        val answered = card.answer
        if (card.status == "answered" && !answered.isNullOrEmpty() && answered !in card.options) {
            Text("你的回答：$answered", fontSize = 13.sp, color = Reeve.extra.ok)
        }
    }
}

@Composable
fun SecretCard(session: BotSession, req: SecretReq) {
    val scope = rememberCoroutineScope()
    var value by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    when (req.status) {
        "saved" -> Text("🔒 已安全保存 ${req.name}（值已加密，不会显示）", fontSize = 13.sp, color = Reeve.extra.ok)
        "cancelled" -> Text("已取消，未保存 ${req.name}", fontSize = 13.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
        else -> Column(
            Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.surfaceContainerHigh, Shapes16).padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(10.dp),
        ) {
            Row(horizontalArrangement = Arrangement.spacedBy(10.dp), verticalAlignment = Alignment.CenterVertically) {
                Icon(Icons.Outlined.Key, null, tint = MaterialTheme.colorScheme.primary)
                Text((if (req.exists) "更新密钥 " else "保存密钥 ") + req.name, style = MaterialTheme.typography.titleSmall)
            }
            if (!req.description.isNullOrEmpty()) Text(req.description, fontSize = 13.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
            OutlinedTextField(
                value, { value = it }, label = { Text("粘贴密钥") }, singleLine = true,
                visualTransformation = PasswordVisualTransformation(),
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
                modifier = Modifier.fillMaxWidth(),
            )
            Text("直接加密保存在服务器上，不进聊天记录，助理也看不到。", fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
            error?.let { Text(it, fontSize = 12.sp, color = MaterialTheme.colorScheme.error) }
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Button(enabled = value.isNotEmpty() && !busy, onClick = {
                    busy = true; error = null
                    scope.launch {
                        runCatching { session.saveSecret(value, requestId = req.id) }.onSuccess { value = "" }.onFailure { error = ServerClient.describe(it) }
                        busy = false
                    }
                }) { Text("保存") }
                OutlinedButton(enabled = !busy, onClick = { scope.launch { runCatching { session.cancelSecretRequest(req.id) } } }) { Text("取消") }
            }
        }
    }
}

@Suppress("unused")
private val unusedColor = Color.Unspecified
