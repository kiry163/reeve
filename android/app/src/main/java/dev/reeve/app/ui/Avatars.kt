package dev.reeve.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import dev.reeve.app.core.BotProfile

/**
 * Bot 头像：App 不打包任何头像资源，统一用首字母占位。
 * 背景色由名称的 hash 从固定调色板里选，同一个 Bot 永远同色（不引入新资源、不依赖服务器字段）。
 */
private val AVATAR_BACKGROUNDS = listOf(
    Color(0xFFE7EFE3),
    Color(0xFFE7EDF6),
    Color(0xFFF6EEE2),
    Color(0xFFF1E8EF),
    Color(0xFFE2F0EF),
    Color(0xFFEFEAE1),
)

internal fun avatarPaletteIndex(name: String): Int {
    var h = 0
    for (c in name) h = h * 31 + c.code
    return (h and Int.MAX_VALUE) % AVATAR_BACKGROUNDS.size
}

@Composable
fun BotAvatar(profile: BotProfile, size: Dp = 40.dp) {
    val background = remember(profile.name) { AVATAR_BACKGROUNDS[avatarPaletteIndex(profile.name)] }
    Box(
        Modifier.size(size).clip(CircleShape).background(background)
            .border(1.dp, MaterialTheme.colorScheme.outlineVariant, CircleShape),
        contentAlignment = Alignment.Center,
    ) {
        Text(profile.name.take(1), color = Color.Black.copy(alpha = 0.6f), fontSize = (size.value * 0.42f).sp)
    }
}

/** 内置 Reeve Agent 的头像；和所有 Bot 一样显示首字母占位。 */
@Composable
fun ReeveAgentAvatar(size: Dp = 40.dp) {
    BotAvatar(BotProfile(id = "reeve", name = "Reeve"), size)
}
