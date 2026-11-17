package dev.reeve.app

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.w3c.dom.Element
import java.io.File
import javax.xml.parsers.DocumentBuilderFactory

/**
 * 直接读源 manifest 断言安全相关声明。单测运行目录是模块根（`android/app`），
 * 不依赖 Android 框架，也不需要 AGP 的 merged manifest。
 */
class ManifestTest {
    private companion object {
        const val ANDROID = "http://schemas.android.com/apk/res/android"
        const val ROUTE_THEME = "@style/Theme.Reeve.Route"
    }

    private val manifest: Element by lazy {
        val file = File("src/main/AndroidManifest.xml")
        assertTrue("找不到 $file", file.isFile)
        DocumentBuilderFactory.newInstance().apply { isNamespaceAware = true }
            .newDocumentBuilder().parse(file).documentElement
    }

    private fun activityNamed(name: String): Element? {
        val nodes = manifest.getElementsByTagName("activity")
        return (0 until nodes.length).map { nodes.item(it) as Element }
            .firstOrNull { it.getAttributeNS(ANDROID, "name") == name }
    }

    @Test
    fun backupsAreDisabled() {
        // 设备会话令牌存在明文 SharedPreferences 里，不能进云备份或 `adb backup`。
        val app = manifest.getElementsByTagName("application").item(0) as Element
        assertEquals("false", app.getAttributeNS(ANDROID, "allowBackup"))
    }

    @Test
    fun notificationRoutingActivityIsNotExported() {
        val route = activityNamed(".RouteActivity")
        assertNotNull("manifest 里缺少 .RouteActivity", route)
        assertEquals("false", route!!.getAttributeNS(ANDROID, "exported"))
        assertEquals(ROUTE_THEME, route.getAttributeNS(ANDROID, "theme"))
    }
}
