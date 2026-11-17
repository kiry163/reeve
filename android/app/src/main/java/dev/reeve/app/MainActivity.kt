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
import dev.reeve.app.ui.ReeveRoot
import dev.reeve.app.ui.ReeveTheme

class MainActivity : ComponentActivity() {
    private val openRef = mutableStateOf<BotRef?>(null)
    private val reeveApp get() = application as ReeveApp
    private val askNotify = registerForActivityResult(ActivityResultContracts.RequestPermission()) {}

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        // 通知点击由 RouteActivity 把 ref 放进 AppState；这里只消费进程内状态，
        // 不从 intent 读 EXTRA_REF，外部 App 无法伪造导航。
        openRef.value = reeveApp.state.takePendingRef()
        // 深链 ACTION_VIEW 只在首次创建时处理：重建会重投同一个 ACTION_VIEW，
        // 那时 Nav 已重建回 Home，再处理一次会重新弹出配对页。
        if (savedInstanceState == null) handleLink(intent)
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
        // 通知点击：RouteActivity 已把 ref 写进 AppState（连点两条通知各开各的）。
        reeveApp.state.takePendingRef()?.let { openRef.value = it }
        handleLink(intent)
    }

    override fun onStart() {
        super.onStart()
        reeveApp.state.foreground = true
    }

    override fun onStop() {
        super.onStop()
        reeveApp.state.foreground = false
    }

    private fun handleLink(intent: Intent?) {
        if (intent?.action != Intent.ACTION_VIEW) return
        // reeve://pair?v=1&host=…（扫码/系统点开链接）；只预填地址，不自动保存。
        // 来源标成外部链接，配对页必须额外确认地址；密码始终由用户输入，不在链接里。
        PendingPair.fromLink(intent.dataString)?.let { reeveApp.state.pendingPair = it }
        // 消费掉链接，重建时不会再当深链处理。
        intent.action = Intent.ACTION_MAIN
        intent.data = null
        setIntent(intent)
    }
}
