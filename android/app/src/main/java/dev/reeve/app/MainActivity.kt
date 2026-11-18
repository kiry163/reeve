package dev.reeve.app

import android.Manifest
import android.content.Intent
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.runtime.mutableStateOf
import dev.reeve.app.core.BotRef
import dev.reeve.app.core.PendingPair
import dev.reeve.app.core.parseOpenLink
import dev.reeve.app.core.resolveOpenRef
import dev.reeve.app.ui.ReeveRoot
import dev.reeve.app.ui.ReeveTheme

class MainActivity : ComponentActivity() {
    private val openRef = mutableStateOf<BotRef?>(null)
    private val reeveApp get() = application as ReeveApp
    private val askNotify = registerForActivityResult(ActivityResultContracts.RequestPermission()) {}

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        // 本地通知点击由 RouteActivity 把 ref 放进 AppState；这里只消费进程内状态，
        // 不从 intent 读 EXTRA_REF，外部 App 无法伪造导航。
        openRef.value = reeveApp.state.takePendingRef()
        // 重建会重投同一个 ACTION_VIEW：pair 只在首次创建时处理，避免重新弹出配对页；
        // open 是幂等导航，重建时也可以重新落到聊天。
        handleLink(intent, allowPair = savedInstanceState == null)
        val state = reeveApp.state
        setContent {
            ReeveTheme(state.appearance) {
                ReeveRoot(state, openRef)
            }
        }
        if (Build.VERSION.SDK_INT >= 33) askNotify.launch(Manifest.permission.POST_NOTIFICATIONS)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        // 本地通知点击：RouteActivity 已把 ref 写进 AppState（连点两条通知各开各的）。
        reeveApp.state.takePendingRef()?.let { openRef.value = it }
        handleLink(intent, allowPair = true)
    }

    override fun onStart() {
        super.onStart()
        reeveApp.state.foreground = true
    }

    override fun onStop() {
        super.onStop()
        reeveApp.state.foreground = false
    }

    private fun handleLink(intent: Intent?, allowPair: Boolean) {
        if (intent?.action != Intent.ACTION_VIEW) return
        val raw = intent.dataString
        when (intent.data?.host?.lowercase()) {
            // reeve://pair?v=1&host=…（扫码/系统点开链接）；只预填地址，不自动保存。
            // 来源标成外部链接，配对页必须额外确认地址；密码始终由用户输入，不在链接里。
            "pair" -> if (allowPair) PendingPair.fromLink(raw)?.let { reeveApp.state.pendingPair = it }
            // reeve://open?bot=…&host=…（ntfy 通知点击）；只导航到已配置的服务器。
            "open" -> {
                val ref = parseOpenLink(raw)?.let { resolveOpenRef(it, reeveApp.state.servers.map { s -> s.config }) }
                if (ref != null) openRef.value = ref
            }
        }
        // 消费掉链接，重建时不会再当深链处理。
        intent.action = Intent.ACTION_MAIN
        intent.data = null
        setIntent(intent)
    }
}
