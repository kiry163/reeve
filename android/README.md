# reeve（Android）

reeve 的原生安卓客户端（Kotlin + Jetpack Compose，Material 3），Android 8.0 以上。

当前照 `docs/android-contract.md` 实现：用服务器密码登录，服务端返回设备会话令牌；后续请求带
`Authorization: Bearer <token>`，401 时提示重新输入密码。配对串 `reeve://pair?v=1&host=<addr>`
只包含地址。聊天、命令面板与设置都走同一套 SSE 事件流归并出的状态。

## 登录与配对

「设置 › 添加服务器」（首次启动也有入口）提供三种方式：

- **扫码**：粘贴 `reeve pair` 打印的配对串，或用任一二维码生成工具把它变成二维码后扫码
  （首次使用会请求相机权限）；
- **粘贴配对串**：读剪贴板里的 `reeve://pair?…`，自动填好地址，不是配对串时提示「配对串无效」；
- **手填**：地址（`IP:端口` 或域名；没写协议时域名按 HTTPS、IP 按 HTTP）加服务器密码。

点「登录并保存」前会先 `GET /api/health` 确认地址可达，再用密码 `POST /api/auth/login` 换取设备会话，
最后 `GET /api/auth/me` 确认；失败会说明是连不上、不是 reeve 还是密码不对。
同一个 `host:端口` 重新登录会复用原条目并保留 Bot 偏好，不同 host 新增一台。

也可以直接点开 `reeve://pair?v=1&host=<addr>` 深链：App 打开预填好地址的配对页，**不会自动保存**。
外部 App / 网页也能触发这个深链，所以深链来源会在页面顶部显示「这些信息来自外部链接，不是你扫码得到的」
和完整地址，必须勾选「我已核对地址」才能保存；扫码、粘贴、手填没有这道提示。密码不在链接里。

`android:allowBackup="false"`：设备会话令牌存在明文 SharedPreferences 里，App 不参与云备份，也不能用
`adb backup` 导出数据。

reeve 没有 TLS：明文 HTTP 下登录密码、设备会话令牌和聊天内容在网络上不加密，只适合可信局域网；
跨网络请用 Tailscale 或 SSH 隧道。

## 通知与推送

### App 内本地通知

三类本地通知，默认全开，开关在「设置 › 通知」：

| 事件 | 渠道 | 文案 |
|---|---|---|
| `approval.requested` | `alerts`（HIGH） | 有一个操作等你确认 |
| `task.completed/failed/cancelled`（含 `task.updated` 终态） | `replies`（DEFAULT） | 任务「x」完成了 / 失败了 / 已取消 |
| 后台收到新的助手消息 | `replies`（DEFAULT） | 消息摘要（≤120 字） |

- **抑制范围**：该 Bot 的任意页面（聊天、渠道、执行器、技能、Bot 编辑）都算「正在看」，都不弹；
  前台看别的页面、后台（即使会话还停在那个页面）都提醒。三类各有开关，默认全开。
- **正文**只放一句摘要，不含会话令牌、不含工具参数原文；摘要在 `notifySummary` 里折叠空白并按 code point 截断到
  120 字符（不切 emoji 的代理对）。
- **锁屏隐私**：通知是 `VISIBILITY_PRIVATE`，公开版本只有 Bot 名，摘要在锁屏上不显示。
- **点击**：`PendingIntent` 指向不导出的 `RouteActivity`（`exported=false`、透明无历史），它把
  `serverId|botId`（`BotRef.key`）放进进程内的 `AppState.pendingRef` 再拉起 `MainActivity`；外部 App
  不能注入 ref。进程被杀后由通知重建仍会打开对应聊天，连点两条通知各开各的。
  `serverId|botId` 解析失败（不是两段）就忽略。
- **去重**：审批、任务终态、新回复三类各自一条通知（id = `Bot + 类别`），同一类后来的覆盖前一条，
  不同类型互不顶掉。
- 本地通知只在 App 存活/打开时有效；前台保活服务已移除。

### 外部推送（ntfy）

服务端在网页「设置 › 推送」里配置自建 ntfy。手机端安装 ntfy（F-Droid 或 GitHub 上**不带 FCM** 的构建），
订阅 `ntfy://<host>/<topic>`，授予通知权限并关闭电池优化（国产 ROM 还要允许自启动）。服务端的认证方式
可选无、访问 token（Bearer）或用户名 + 密码（HTTP Basic）；如果 ntfy 开了访问控制，手机 App 里也要填
同一份凭据。然后在 App「设置 › 通知」里打开「使用 ntfy 外部推送」。

开启后：

- 服务端在审批请求、任务终态、助手最终回复时推一条极简通知，不含聊天正文和工具参数；
- App 不再弹本地通知，三个目录开关变灰；
- 点击通知走 `reeve://open?bot=…&host=…`：`host` 来自网页端「客户端访问地址」，要和 App 里配置的
  服务器地址一致；匹配不到时只打开 App，不跳聊天；
- App 进程被杀也能由 ntfy App 送达；通知的正文、去重和锁屏样式由 ntfy App 管理；
- 不依赖 FCM/Google。

已知限制：

1. **通知不会自动撤销**：审批在别处（网页端）处理掉、任务在 App 里取消之后，之前发出的通知仍留在通知栏，
   需要点开或手动划掉；ntfy 通知同样不会自动撤销。
2. **本地通知同类只有最新一条**：同一 Bot 连发两条新回复时，后一条替换前一条（通知栏只留最后一条），
   历史都在聊天页里；ntfy 通知由 ntfy App 管理，可能堆叠。
3. **ntfy App 必须活着**：被系统杀掉、没有电池白名单或网络不可达时，通知会延迟或丢失。

## 编译 APK

需要 JDK 17+ 和 Android SDK（compileSdk 35）。

```bash
cd android
JAVA_HOME=/opt/homebrew/opt/openjdk@17 ANDROID_HOME=/path/to/Android/sdk \
  ./gradlew --no-daemon :app:assembleRelease -PreeveVersion=0.4.0
```

APK 输出到 `app/build/outputs/apk/release/app-release.apk`。正式包必须使用固定的 PKCS#12
签名密钥，由 `REEVE_ANDROID_KEYSTORE`（文件路径）、`REEVE_ANDROID_KEYSTORE_PASSWORD`、
`REEVE_ANDROID_KEY_ALIAS` 和 `REEVE_ANDROID_KEY_PASSWORD` 配置。没有这些变量时 release 构建
**保持未签名**，产物是 `app-release-unsigned.apk`，不能分发（`release.sh` 会直接跳过 APK，
不会用 debug 密钥凑数）。`assembleDebug` 的测试包使用 AGP 的 debug 密钥，不用于发布。
`reeveVersion` 默认为 `0.0.0`，`versionCode = major*1_000_000 + minor*1_000 + patch`。

发布流程见仓库根目录的 `docs/release.md`。
