package dev.reeve.app

import dev.reeve.app.core.BotRef
import dev.reeve.app.core.OpenLink
import dev.reeve.app.core.ServerConfig
import dev.reeve.app.core.parseOpenLink
import dev.reeve.app.core.resolveOpenRef
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertNotNull
import org.junit.Test

/** `reeve://open` 只做导航：解析 Bot/host，再落到已配置的服务器。 */
class OpenLinkTest {
    private fun server(id: String, url: String) = ServerConfig(id, id, url, "token")

    @Test
    fun parsesBotAndEncodedHost() {
        val link = parseOpenLink("reeve://open?v=1&bot=main&host=home.example.com%3A18800")
        assertNotNull(link)
        assertEquals("main", link!!.bot)
        assertEquals("home.example.com:18800", link.host)
    }

    @Test
    fun rejectsWrongSchemeHostOrMissingBot() {
        assertNull(parseOpenLink("reeve://pair?v=1&bot=main"))
        assertNull(parseOpenLink("https://open?bot=main"))
        assertNull(parseOpenLink("reeve://open?host=home.example.com"))
        assertNull(parseOpenLink(null))
    }

    @Test
    fun resolvesMatchingHostAndDefaultPort() {
        val s = server("srv-1", "https://home.example.com")
        assertEquals(BotRef("srv-1", "main"), resolveOpenRef(OpenLink("main", "home.example.com"), listOf(s)))
        assertEquals(BotRef("srv-1", "main"), resolveOpenRef(OpenLink("main", "home.example.com:443"), listOf(s)))
    }

    @Test
    fun resolvesExplicitPortAndRejectsMismatch() {
        val s = server("srv-1", "http://192.168.1.20:18800")
        assertEquals(BotRef("srv-1", "main"), resolveOpenRef(OpenLink("main", "192.168.1.20:18800"), listOf(s)))
        assertNull(resolveOpenRef(OpenLink("main", "192.168.1.20:18801"), listOf(s)))
        assertNull(resolveOpenRef(OpenLink("main", "192.168.1.20"), listOf(s)))
    }

    @Test
    fun hostlessOnlyWorksWithSingleServer() {
        val a = server("a", "https://a.example.com")
        val b = server("b", "https://b.example.com")
        assertNull(resolveOpenRef(OpenLink("main", null), listOf(a, b)))
        assertEquals(BotRef("a", "main"), resolveOpenRef(OpenLink("main", null), listOf(a)))
    }

    @Test
    fun unknownHostOpensNothing() {
        assertNull(resolveOpenRef(OpenLink("main", "evil.example"), listOf(server("a", "https://a.example.com"))))
    }
}
