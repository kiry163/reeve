package dev.reeve.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.outlined.Terminal
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import dev.reeve.app.core.CommandInfo

/** 输入框里正在敲命令时，返回 `/` 后的第一段（大小写无关的查询串）；否则 null。 */
fun commandQuery(text: String): String? {
    if (!text.startsWith("/") || text.contains('\n')) return null
    return text.drop(1).substringBefore(' ').lowercase()
}

/** 候选：按 name/aliases/summary 过滤，大小写不敏感，最多 8 条。 */
fun filterCommands(commands: List<CommandInfo>, query: String, limit: Int = 8): List<CommandInfo> {
    val q = query.trim().removePrefix("/").lowercase()
    return commands.asSequence().filter { c ->
        q.isEmpty() ||
            c.name.lowercase().contains(q) ||
            c.aliases.any { it.lowercase().contains(q) } ||
            c.summary.lowercase().contains(q)
    }.take(limit).toList()
}

/**
 * 命令面板：输入框以 `/` 开头时显示在输入框上方；也可以从独立入口打开。
 * 点候选时：已经有参数 → 执行整行；只有命令名 → 补一个空格等参数。
 */
@Composable
fun CommandPanel(
    commands: List<CommandInfo>,
    onComplete: (String) -> Unit,
    modifier: Modifier = Modifier,
    query: String? = null,
    title: String? = null,
    onDismiss: (() -> Unit)? = null,
) {
    val list = remember(commands, query) { filterCommands(commands, query.orEmpty()) }
    if (list.isEmpty()) return
    Column(
        modifier.fillMaxWidth().background(MaterialTheme.colorScheme.surfaceContainerHigh, Shapes16).padding(vertical = 6.dp),
    ) {
        Row(
            Modifier.fillMaxWidth().padding(start = 16.dp, end = if (onDismiss != null) 4.dp else 16.dp),
            verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            Icon(Icons.Outlined.Terminal, null, Modifier.size(16.dp), tint = MaterialTheme.colorScheme.primary)
            Text(title ?: "命令", style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f))
            onDismiss?.let { IconButton(onClick = it) { Icon(Icons.Filled.Close, "关闭", Modifier.size(18.dp)) } }
        }
        Text(
            "命令直接生效，不走模型", fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.padding(start = 16.dp, end = 16.dp, bottom = 4.dp),
        )
        list.forEach { c ->
            Row(
                Modifier.fillMaxWidth()
                    .clickable { onComplete("/" + c.name + " ") }
                    .padding(horizontal = 16.dp, vertical = 8.dp),
                verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp),
            ) {
                Text(
                    "/" + c.name, fontFamily = FontFamily.Monospace, fontSize = 13.sp,
                    color = MaterialTheme.colorScheme.primary,
                )
                Text(
                    c.summary, fontSize = 12.5.sp, color = MaterialTheme.colorScheme.onSurfaceVariant,
                    maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f),
                )
                if (c.usage.isNotEmpty()) {
                    Text(c.usage, fontFamily = FontFamily.Monospace, fontSize = 11.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
        }
    }
}

/** 输入框旁边独立入口打开的完整命令列表（同样最多 8 条）。 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun CommandSheet(
    commands: List<CommandInfo>,
    onComplete: (String) -> Unit,
    onClose: () -> Unit,
) {
    ModalBottomSheet(onDismissRequest = onClose) {
        Column(Modifier.fillMaxWidth().padding(bottom = 24.dp)) {
            Text(
                "命令", style = MaterialTheme.typography.titleLarge,
                modifier = Modifier.padding(start = 24.dp, end = 24.dp, bottom = 8.dp),
            )
            CommandPanel(
                commands = commands, query = null,
                onComplete = { onComplete(it); onClose() },
            )
        }
    }
}
