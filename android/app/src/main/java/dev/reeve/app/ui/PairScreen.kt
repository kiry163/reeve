package dev.reeve.app.ui

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.provider.Settings
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Visibility
import androidx.compose.material.icons.filled.VisibilityOff
import androidx.compose.material.icons.outlined.ContentPaste
import androidx.compose.material.icons.outlined.Info
import androidx.compose.material.icons.outlined.QrCodeScanner
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.core.content.ContextCompat
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import dev.reeve.app.core.AppState
import dev.reeve.app.core.PairDraft
import dev.reeve.app.core.ServerClient
import dev.reeve.app.core.ServerConfig
import dev.reeve.app.core.ServerEntry
import dev.reeve.app.core.canPair
import kotlinx.coroutines.launch

/**
 * 配对页：扫码、粘贴配对串、手填三种入口都落到同一份草稿。
 *
 * 深链 `reeve://pair?…` 与「编辑服务器 / 重新配对」通过 [initial] / [existing] 预填，**不会自动保存**：
 * 用户必须点「配对」。编辑模式同样能扫码或粘贴，token 轮换时不用手抄。
 *
 * [fromExternalLink] 为 true 时（地址和 token 来自外部 App / 网页触发的深链），页面顶部展示完整
 * 地址警告，并要求勾选「我已核对地址」才能保存；扫码 / 粘贴 / 手填不走这条路径。
 */
@Composable
fun PairScreen(
    state: AppState,
    nav: Nav,
    existing: ServerEntry? = null,
    initial: PairDraft? = null,
    fromExternalLink: Boolean = false,
) {
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    val clipboard = LocalClipboardManager.current
    val start = remember { initial ?: existing?.let { PairDraft.fromConfig(it.config) } ?: PairDraft() }
    // 手填内容尽量不丢；token 不进 savedInstanceState，只在内存里
    var name by rememberSaveable { mutableStateOf(start.name) }
    var address by rememberSaveable { mutableStateOf(start.address) }
    var token by remember { mutableStateOf(start.token) }
    var show by remember { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var cameraBlocked by remember { mutableStateOf(false) }
    // 外部链接来源必须由用户勾选确认地址；换了草稿就重新确认。
    var addressConfirmed by rememberSaveable { mutableStateOf(false) }
    val editing = existing != null
    val insecure = insecureAddress(address)

    fun apply(draft: PairDraft) {
        address = draft.address
        token = draft.token
        if (draft.name.isNotBlank()) name = draft.name
        addressConfirmed = false
        error = null
        cameraBlocked = false
    }

    val scan = rememberLauncherForActivityResult(ScanContract()) { result ->
        val text = result.contents
        if (text == null) return@rememberLauncherForActivityResult // 用户取消
        val draft = PairDraft.fromPairString(text)
        if (draft == null) error = "配对串无效：二维码里不是 reeve://pair?… 配对码" else apply(draft)
    }

    val launchScan: () -> Unit = {
        scan.launch(
            ScanOptions().apply {
                setDesiredBarcodeFormats(ScanOptions.QR_CODE)
                setPrompt("把 reeve 的配对二维码放进框内")
                setBeepEnabled(false)
                setOrientationLocked(false)
            },
        )
    }

    // 自己先要相机权限：zxing 默认的被拒文案（「相机出现问题，可能需要重启设备」）会误导永久拒绝的用户
    val askCamera = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (granted) {
            cameraBlocked = false
            launchScan()
        } else {
            cameraBlocked = true
        }
    }

    fun startScan() {
        val granted = ContextCompat.checkSelfPermission(context, Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED
        if (granted) {
            cameraBlocked = false
            launchScan()
        } else {
            askCamera.launch(Manifest.permission.CAMERA)
        }
    }

    SubPage(if (editing) "编辑服务器" else "配对服务器", nav) {
        Column(Modifier.padding(horizontal = 16.dp), verticalArrangement = Arrangement.spacedBy(14.dp)) {
            if (fromExternalLink) {
                Column(
                    Modifier.fillMaxWidth().border(1.dp, MaterialTheme.colorScheme.error, Shapes12).padding(12.dp),
                    verticalArrangement = Arrangement.spacedBy(6.dp),
                ) {
                    Text(
                        "这些信息来自外部链接，不是你扫码得到的",
                        color = MaterialTheme.colorScheme.error, fontWeight = FontWeight.Medium,
                    )
                    // 完整地址不省略：用户要能看清到底连到哪台服务器
                    Text("地址：$address", fontSize = 14.sp, color = MaterialTheme.colorScheme.onSurface)
                    Text(
                        "外部 App 或网页可以伪造这个链接。只在你确定这个地址就是自己的 reeve 服务器时才继续。",
                        fontSize = 13.sp, color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                }
            }
            Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                OutlinedButton(onClick = { startScan() }, modifier = Modifier.weight(1f)) {
                    Icon(Icons.Outlined.QrCodeScanner, null)
                    Spacer(Modifier.width(6.dp))
                    Text("扫码")
                }
                OutlinedButton(
                    onClick = {
                        val draft = PairDraft.fromPairString(clipboard.getText()?.text)
                        if (draft == null) error = "配对串无效：剪贴板里没有 reeve://pair?… 格式的配对串"
                        else apply(draft)
                    },
                    modifier = Modifier.weight(1f),
                ) {
                    Icon(Icons.Outlined.ContentPaste, null)
                    Spacer(Modifier.width(6.dp))
                    Text("粘贴配对串")
                }
            }
            if (cameraBlocked) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(
                        "没有相机权限，没法扫码。可以在系统设置里允许相机，或者直接粘贴配对串 / 手填地址和 token。",
                        fontSize = 13.sp, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.weight(1f),
                    )
                    TextButton(
                        onClick = {
                            runCatching {
                                context.startActivity(
                                    Intent(
                                        Settings.ACTION_APPLICATION_DETAILS_SETTINGS,
                                        Uri.fromParts("package", context.packageName, null),
                                    ),
                                )
                            }
                        },
                    ) { Text("去设置") }
                }
            }
            Text(
                if (editing) "重新配对会替换这枚 token。可以扫码、粘贴新的配对串，或改地址 / token 后保存。"
                else if (address.isBlank() && token.isBlank()) "扫码或粘贴配对串会自动填好地址和 token；也可以直接手填。"
                else "已填入配对串里的地址和 token，确认后点「配对」。",
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            OutlinedTextField(
                name, { name = it }, label = { Text("名称（可选）") }, placeholder = { Text("东京 VPS") },
                singleLine = true, modifier = Modifier.fillMaxWidth(),
            )
            OutlinedTextField(
                address, { address = it }, label = { Text("地址") }, placeholder = { Text("bot.example.com 或 192.168.1.5:18800") },
                singleLine = true,
                supportingText = { Text("没写协议时，域名按 HTTPS、IP 按 HTTP") },
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri), modifier = Modifier.fillMaxWidth(),
            )
            OutlinedTextField(
                token, { token = it }, label = { Text("Token") }, placeholder = { Text("配对串里的 token") }, singleLine = true,
                modifier = Modifier.fillMaxWidth(),
                visualTransformation = if (show) VisualTransformation.None else PasswordVisualTransformation(),
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
                trailingIcon = {
                    IconButton(onClick = { show = !show }) {
                        Icon(if (show) Icons.Filled.VisibilityOff else Icons.Filled.Visibility, "显示 token")
                    }
                },
            )
            if (insecure) {
                Text(
                    "这是明文 HTTP 连接，token 和聊天内容在网络上不加密。reeve 没有 TLS：请只在可信局域网使用，" +
                        "跨网络请用 Tailscale 或 SSH 隧道。",
                    color = Reeve.extra.warn, fontSize = 13.sp,
                )
            }
            error?.let { Text(it, color = MaterialTheme.colorScheme.error, fontSize = 13.sp) }
            if (fromExternalLink) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Checkbox(checked = addressConfirmed, onCheckedChange = { addressConfirmed = it })
                    Text("我已核对地址", fontSize = 14.sp)
                }
            }
            Button(
                onClick = {
                    busy = true; error = null
                    scope.launch {
                        runCatching { state.pair(PairDraft(address, token, name), editing = existing) }
                            .onSuccess { nav.pop() }
                            .onFailure { error = ServerClient.describe(it) }
                        busy = false
                    }
                },
                enabled = canPair(address, token, fromExternalLink, addressConfirmed, busy),
                modifier = Modifier.fillMaxWidth(),
            ) { Text(if (busy) "正在校验…" else "配对") }
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Icon(Icons.Outlined.Info, null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
                Text(
                    "token 由 reeve 生成（运行 reeve token show，或看安装完打印的配对串），手机只保存这一份。" +
                        "保存前会先检查服务器是否可达、token 是否有效；同一台服务器重新配对会保留原有 Bot 设置。",
                    fontSize = 13.sp, color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        }
    }
}

/** 地址是否「明文 HTTP 且不是本机」——与 [ServerConfig.insecure] 同一套判断 */
private fun insecureAddress(raw: String): Boolean = runCatching {
    ServerConfig("", "", ServerConfig.normalize(raw), "").insecure
}.getOrDefault(false)
