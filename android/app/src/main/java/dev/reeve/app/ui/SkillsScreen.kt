package dev.reeve.app.ui

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.Description
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import dev.reeve.app.core.AppState
import dev.reeve.app.core.BotRef
import dev.reeve.app.core.ServerClient
import dev.reeve.app.core.SkillsIndex

/** 技能索引只读：技能在 reeve 启动时从技能目录加载。 */
@Composable
fun SkillsScreen(state: AppState, initial: BotRef?, nav: Nav) {
    var selected by remember(initial) { mutableStateOf(initial ?: defaultBotRef(state)) }
    val session = selected?.let { state.session(it) }
    var index by remember { mutableStateOf<SkillsIndex?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var reload by remember { mutableStateOf(0) }

    LaunchedEffect(session?.ref, reload) {
        val s = session ?: return@LaunchedEffect
        index = null
        error = null
        runCatching { s.loadSkills() }
            .onSuccess { index = it }
            .onFailure { error = ServerClient.describe(it) }
    }

    SubPage(
        "技能", nav, subtitle = selected?.let { state.session(it)?.profile?.name ?: it.bot },
        actions = {
            TextButton(enabled = session != null, onClick = { reload++ }) { Text("重新加载") }
        },
    ) {
        BotPickerRow(state, selected, onPick = { selected = it })
        Note("技能在 reeve 启动时从服务器的技能目录加载，这里只能查看，不能编辑。")
        if (session == null) {
            Note("这个 Bot 现在连不上，读取不了技能索引。")
            return@SubPage
        }
        val idx = index
        if (idx == null) {
            if (error == null) Busy() else Note(error!!, MaterialTheme.colorScheme.error)
            return@SubPage
        }
        if (idx.errors.isNotEmpty()) {
            SectionLabel("加载失败")
            idx.errors.forEach { Note(it, Reeve.extra.warn) }
        }
        if (idx.skills.isEmpty()) {
            Note("还没有加载到技能。把技能放在服务器的 skills/<name>/SKILL.md（实例级所有 Bot 共用；Bot 目录下的 skills/ 只对这个 Bot 生效），重启 reeve 后出现在这里。")
        }
        idx.skills.forEach { sk ->
            ListRow(sk.name, sk.description, Icons.Outlined.Description)
            Column(Modifier.padding(horizontal = 16.dp, vertical = 2.dp)) {
                if (sk.whenToUse.isNotEmpty()) Text("何时使用：${sk.whenToUse}", fontSize = 13.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
                if (sk.path.isNotEmpty()) Text(sk.path, fontSize = 12.sp, fontFamily = FontFamily.Monospace, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
        if (idx.skills.isNotEmpty()) Note("共 ${idx.skills.size} 个技能。")
    }
}
