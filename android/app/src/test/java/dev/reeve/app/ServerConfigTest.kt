package dev.reeve.app

import dev.reeve.app.core.ServerConfig
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Test

class ServerConfigTest {
    @Test
    fun normalizesBareHostWithPort() {
        assertEquals("http://192.168.1.5:18800", ServerConfig.normalize("192.168.1.5:18800"))
    }

    @Test
    fun keepsHttpsForDomain() {
        assertEquals("https://reeve.example.com", ServerConfig.normalize("reeve.example.com"))
    }

    @Test
    fun stripsTrailingSlash() {
        assertEquals("http://host:18800", ServerConfig.normalize("http://host:18800/"))
    }

    @Test
    fun normalizesLocalhostToHttp() {
        assertEquals("http://localhost:18800", ServerConfig.normalize("localhost:18800"))
    }

    @Test
    fun rejectsEmptyAddress() {
        assertThrows(IllegalArgumentException::class.java) { ServerConfig.normalize("   ") }
    }

    @Test
    fun parsesAddressOnlyPairUri() {
        val c = ServerConfig.fromPairUri("reeve://pair?v=1&host=192.168.1.5:18800")
        assertEquals("http://192.168.1.5:18800", c?.url)
        assertEquals("", c?.token)
    }

    @Test
    fun parsesTlsHost() {
        val c = ServerConfig.fromPairUri("reeve://pair?v=1&host=reeve.example.com")
        assertEquals("https://reeve.example.com", c?.url)
    }

    @Test
    fun ignoresLegacyTokenParam() {
        // 旧二维码/旧链接里可能还带 token；只取地址，密码必须由用户输入。
        val c = ServerConfig.fromPairUri("reeve://pair?v=1&host=192.168.1.5:18800&token=abc")
        assertEquals("http://192.168.1.5:18800", c?.url)
        assertEquals("", c?.token)
    }

    @Test
    fun rejectsUriWithoutScheme() {
        assertNull(ServerConfig.fromPairUri("pair?v=1&host=x"))
        assertNull(ServerConfig.fromPairUri("//pair?v=1&host=x"))
    }

    @Test
    fun rejectsOpaqueUri() {
        assertNull(ServerConfig.fromPairUri("reeve:pair?v=1&host=x"))
    }

    @Test
    fun rejectsQueryOnlyUri() {
        assertNull(ServerConfig.fromPairUri("?v=1&host=x"))
        assertNull(ServerConfig.fromPairUri(""))
    }

    @Test
    fun rejectsWrongSchemeOrVersion() {
        assertNull(ServerConfig.fromPairUri("https://pair?v=1&host=x"))
        assertNull(ServerConfig.fromPairUri("reeve://pair?v=2&host=x"))
        assertNull(ServerConfig.fromPairUri("reeve://pair?host=x"))
        assertNull(ServerConfig.fromPairUri("reeve://pair?v=1&host="))
        assertNull(ServerConfig.fromPairUri("reeve://other?v=1&host=x"))
        assertNull(ServerConfig.fromPairUri("not a uri"))
    }
}
