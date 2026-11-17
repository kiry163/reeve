package dev.reeve.app.ui

import androidx.activity.compose.BackHandler
import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.padding
import androidx.compose.ui.unit.dp
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Chat
import androidx.compose.material.icons.filled.Schedule
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.outlined.Chat
import androidx.compose.material.icons.outlined.Schedule
import androidx.compose.material.icons.outlined.Settings
import androidx.compose.material3.Badge
import androidx.compose.material3.BadgedBox
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.MutableState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import dev.reeve.app.core.AppState
import dev.reeve.app.core.BotRef
import dev.reeve.app.core.PairDraft
import dev.reeve.app.core.ServerStatus

/** 页面：底部三个标签（聊天 / 调度器 / 设置），其余页面压栈打开 */
sealed interface Screen {
    data object Home : Screen
    data object Scheduler : Screen
    data object Settings : Screen
    data class Chat(val ref: BotRef) : Screen
    data class Server(val id: String) : Screen
    data class AddServer(
        val existing: String? = null,
        val draft: PairDraft? = null,
        /** 草稿来自外部深链：配对页要展示完整地址并要求用户勾选确认 */
        val fromExternalLink: Boolean = false,
    ) : Screen
    data class Channels(val ref: BotRef) : Screen
    data class Env(val ref: BotRef? = null) : Screen
    data class Executors(val ref: BotRef? = null) : Screen
    data class Skills(val ref: BotRef? = null) : Screen
    data class BotEditor(val server: String, val ref: BotRef? = null) : Screen
    data object Appearance : Screen
    data object Notifications : Screen
}

class Nav {
    val stack = mutableStateListOf<Screen>(Screen.Home)
    val top: Screen get() = stack.last()
    fun push(s: Screen) { stack.add(s) }
    fun pop() { if (stack.size > 1) stack.removeAt(stack.lastIndex) }
    fun tab(s: Screen) { stack.clear(); stack.add(s) }
    fun replaceTop(s: Screen) { stack[stack.lastIndex] = s }
}

/** 深链入栈去重：栈顶已经是配对页时不再压一层（同一 ACTION_VIEW 被重投时保持原样） */
internal fun shouldOpenPair(top: Screen): Boolean = top !is Screen.AddServer

/**
 * 正在看的 Bot：聊天、渠道、环境变量、执行器、技能、Bot 编辑页都算（新建 Bot 时 ref 为空，不算）。
 * 这些页面上收到的审批/任务/回复都不该再弹通知——用户已经在为这个 Bot 干活了。
 */
internal fun botOf(top: Screen): BotRef? = when (top) {
    is Screen.Chat -> top.ref
    is Screen.Channels -> top.ref
    is Screen.Env -> top.ref
    is Screen.Executors -> top.ref
    is Screen.Skills -> top.ref
    is Screen.BotEditor -> top.ref
    else -> null
}

/**
 * 聊天/渠道页找不到会话时的提示。冷启动（由通知重建）时 SSE 会话要等第一次 `/api/bots`
 * 拉完才建起来，这段时间别报「连不上」，先说「正在连接…」。
 */
internal fun missingChatText(status: ServerStatus?, botKnown: Boolean): String = when (status) {
    null -> "这台服务器已移除"
    ServerStatus.Connecting -> "正在连接…"
    ServerStatus.Online -> if (botKnown) "正在连接…" else "这个 Bot 现在连不上"
    ServerStatus.NeedsPairing -> "token 已失效，需要重新配对"
    is ServerStatus.Offline -> "这个 Bot 现在连不上：${status.error}"
}

@Composable
fun ReeveRoot(state: AppState, openRef: MutableState<BotRef?>) {
    val nav = remember { Nav() }
    BackHandler(enabled = nav.stack.size > 1) { nav.pop() }

    LaunchedEffect(openRef.value) {
        openRef.value?.let { nav.tab(Screen.Home); nav.push(Screen.Chat(it)); openRef.value = null }
    }
    // 深链 reeve://pair?… 只预填配对页，不自动保存
    LaunchedEffect(state.pendingPair) {
        state.pendingPair?.let {
            if (shouldOpenPair(nav.top)) nav.push(Screen.AddServer(draft = it.draft, fromExternalLink = it.fromExternalLink))
            state.pendingPair = null
        }
    }
    val top = nav.top
    LaunchedEffect(top) {
        // 该 Bot 的任意页面都算「正在看」，抑制范围与 README/设置页说明一致。
        state.viewing = botOf(top)
        (top as? Screen.Chat)?.ref?.let { state.markRead(it) }
    }

    if (state.servers.isEmpty() && top == Screen.Home) {
        FirstRunScreen(onAdd = { nav.push(Screen.AddServer()) }, onSettings = { nav.tab(Screen.Settings) })
        return
    }

    val tabbed = top == Screen.Home || top == Screen.Scheduler || top == Screen.Settings
    Scaffold(
        bottomBar = { if (tabbed) BottomBar(state, nav) },
        containerColor = MaterialTheme.colorScheme.surface,
    ) { pad ->
        Box(Modifier.fillMaxSize().padding(if (tabbed) pad else PaddingValues(0.dp))) {
            AnimatedContent(top, transitionSpec = { fadeIn() togetherWith fadeOut() }, label = "screen") { s ->
                ScreenContent(s, state, nav)
            }
        }
    }
}

@Composable
private fun ScreenContent(s: Screen, state: AppState, nav: Nav) {
    when (s) {
        Screen.Home -> HomeScreen(state, nav)
        Screen.Scheduler -> SchedulerScreen(state, nav)
        Screen.Settings -> SettingsScreen(state, nav)
        is Screen.Chat -> {
            val session = state.session(s.ref)
            if (session != null) ChatScreen(state, session, nav)
            else MissingScreen(missingChatText(state.server(s.ref.server)?.status, state.knownBot(s.ref)), nav)
        }
        is Screen.Server -> state.server(s.id)?.let { ServerScreen(state, it, nav) } ?: MissingScreen("服务器已移除", nav)
        is Screen.AddServer -> PairScreen(state, nav, s.existing?.let { state.server(it) }, s.draft, s.fromExternalLink)
        is Screen.Channels -> state.session(s.ref)?.let { ChannelsScreen(it, nav) }
            ?: MissingScreen(missingChatText(state.server(s.ref.server)?.status, state.knownBot(s.ref)), nav)
        is Screen.Env -> EnvScreen(state, s.ref, nav)
        is Screen.Executors -> ExecutorsScreen(state, s.ref, nav)
        is Screen.Skills -> SkillsScreen(state, s.ref, nav)
        is Screen.BotEditor -> state.server(s.server)?.let { BotEditorScreen(state, it, s.ref, nav) } ?: MissingScreen("服务器已移除", nav)
        Screen.Appearance -> AppearanceScreen(state, nav)
        Screen.Notifications -> NotificationsScreen(state, nav)
    }
}

@Composable
private fun BottomBar(state: AppState, nav: Nav) {
    val top = nav.top
    NavigationBar(containerColor = MaterialTheme.colorScheme.surfaceContainer) {
        NavigationBarItem(
            selected = top == Screen.Home, onClick = { nav.tab(Screen.Home) },
            icon = {
                val n = state.totalUnread
                BadgedBox(badge = { if (n > 0) Badge { Text(if (n > 99) "99+" else "$n") } }) {
                    Icon(if (top == Screen.Home) Icons.Filled.Chat else Icons.Outlined.Chat, null)
                }
            },
            label = { Text("聊天") },
        )
        NavigationBarItem(
            selected = top == Screen.Scheduler, onClick = { nav.tab(Screen.Scheduler) },
            icon = { Icon(if (top == Screen.Scheduler) Icons.Filled.Schedule else Icons.Outlined.Schedule, null) },
            label = { Text("调度器") },
        )
        NavigationBarItem(
            selected = top == Screen.Settings, onClick = { nav.tab(Screen.Settings) },
            icon = { Icon(if (top == Screen.Settings) Icons.Filled.Settings else Icons.Outlined.Settings, null) },
            label = { Text("设置") },
        )
    }
}
