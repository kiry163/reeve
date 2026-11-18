package dev.reeve.app

import android.Manifest
import android.app.Application
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import dev.reeve.app.core.AppState
import dev.reeve.app.core.BotSession
import dev.reeve.app.core.CH_ALERTS
import dev.reeve.app.core.CH_REPLIES
import dev.reeve.app.core.NewsKind
import dev.reeve.app.core.notifyChannel
import dev.reeve.app.core.notifySummary
import dev.reeve.app.core.noticeTag
import dev.reeve.app.core.noticeTexts

class ReeveApp : Application() {
    lateinit var state: AppState
        private set

    override fun onCreate() {
        super.onCreate()
        instance = this
        val nm = getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(NotificationChannel(CH_ALERTS, "等你确认", NotificationManager.IMPORTANCE_HIGH).apply {
            description = "有审批请求需要你决定"
        })
        nm.createNotificationChannel(NotificationChannel(CH_REPLIES, "任务与回复", NotificationManager.IMPORTANCE_DEFAULT).apply {
            description = "任务结束、助手的新回复"
        })
        // 旧版本的前台保活渠道：升级后把常驻通知一起清掉。
        nm.deleteNotificationChannel("keepalive")
        state = AppState(this)
        state.onNotify = ::postNotice
    }

    /** 正文只有一句摘要（≤ [dev.reeve.app.core.SUMMARY_LIMIT] 字），不含会话令牌与工具参数原文。 */
    private fun postNotice(session: BotSession, text: String, kind: NewsKind) {
        if (Build.VERSION.SDK_INT >= 33 &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) return
        val body = notifySummary(text)
        if (body.isEmpty()) return
        val channel = notifyChannel(kind)
        // 只指向不导出的 RouteActivity：外部 Intent 无法直接带 ref 拉起 MainActivity。
        val open = Intent(this, RouteActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP
            putExtra(RouteActivity.EXTRA_REF, session.ref.key)
        }
        // 审批/任务/回复各一条，同类的后一条覆盖前一条，互不顶掉。
        val id = (session.ref.key + "|" + noticeTag(kind)).hashCode()
        val pi = PendingIntent.getActivity(this, id, open, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val texts = noticeTexts(session.profile.name, session.server.name, state.servers.size)
        // 锁屏公开版本只有 Bot 名：摘要、会话令牌、工具参数都不出现在系统锁屏上。
        val public = NotificationCompat.Builder(this, channel)
            .setSmallIcon(R.drawable.ic_stat_reeve)
            .setContentTitle(texts.publicTitle)
            .build()
        val n = NotificationCompat.Builder(this, channel)
            .setSmallIcon(R.drawable.ic_stat_reeve)
            .setContentTitle(texts.title)
            .setContentText(body)
            .setStyle(NotificationCompat.BigTextStyle().bigText(body))
            .setVisibility(NotificationCompat.VISIBILITY_PRIVATE)
            .setPublicVersion(public)
            .setContentIntent(pi)
            .setAutoCancel(true)
            .setCategory(if (kind == NewsKind.Approval) NotificationCompat.CATEGORY_REMINDER else NotificationCompat.CATEGORY_MESSAGE)
            .build()
        try {
            NotificationManagerCompat.from(this).notify(id, n)
        } catch (_: SecurityException) {
        }
    }

    companion object {
        lateinit var instance: ReeveApp
            private set
    }
}
