package dev.reeve.app.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/** 首字母头像的颜色只由名称决定：同一个 Bot 每次渲染同色，不依赖已删除的 assets。 */
class AvatarTest {
    @Test
    fun paletteIndexIsStableForTheSameName() {
        assertEquals(avatarPaletteIndex("reeve"), avatarPaletteIndex("reeve"))
        assertEquals(avatarPaletteIndex("东京 VPS"), avatarPaletteIndex("东京 VPS"))
    }

    @Test
    fun paletteIndexAlwaysFallsInsidePalette() {
        for (name in listOf("", "a", "reeve", "Bot 123", "东京 VPS", "x".repeat(200))) {
            val index = avatarPaletteIndex(name)
            assertTrue("$name -> $index", index in 0..5)
        }
    }

    @Test
    fun differentNamesDoNotCollapseToOneColor() {
        assertNotEquals(avatarPaletteIndex("reeve"), avatarPaletteIndex("M4"))
        assertNotEquals(avatarPaletteIndex("alpha"), avatarPaletteIndex("beta"))
    }
}
