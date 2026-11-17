package dev.reeve.app

import android.app.Activity
import android.content.Intent
import android.os.Bundle
import dev.reeve.app.core.BotRef

/**
 * 通知点击的唯一入口（`exported=false`，外部 App 无法启动）。
 *
 * 之前通知直接把 `EXTRA_REF` 交给导出的 [MainActivity]，任何外部 App 都能带同一个 extra
 * 把用户引到任意聊天。现在 ref 只在这里读出并放进进程内的 `AppState.pendingRef`，
 * 再拉起 MainActivity；Activity 本身透明、无历史记录，立刻 `finish()`。
 */
class RouteActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        (application as ReeveApp).state.pendingRef = intent?.getStringExtra(EXTRA_REF)?.let(BotRef::parse)
        // 进程被杀时 MainActivity 会重建；singleTask 的 MainActivity 已在栈里时走 onNewIntent。
        startActivity(
            Intent(this, MainActivity::class.java).apply {
                flags = Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP
            },
        )
        finish()
    }

    companion object {
        /** 通知点开时带的 `serverId|botId`（[BotRef.key]）。 */
        const val EXTRA_REF = "ref"
    }
}
