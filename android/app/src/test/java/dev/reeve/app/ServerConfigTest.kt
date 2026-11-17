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
    fun parsesPairUri() {
        val c = ServerConfig.fromPairUri("reeve://pair?v=1&host=192.168.1.5:18800&token=abc")
        assertEquals("http://192.168.1.5:18800", c?.url)
        assertEquals("abc", c?.token)
    }

    @Test
    fun parsesPairUriWithTlsHostAndEncodedToken() {
        val c = ServerConfig.fromPairUri("reeve://pair?v=1&host=reeve.example.com&token=a_b-c%3D")
        assertEquals("https://reeve.example.com", c?.url)
        assertEquals("a_b-c=", c?.token)
    }

    @Test
    fun rejectsUriWithoutScheme() {
        assertNull(ServerConfig.fromPairUri("pair?v=1&host=x&token=y"))
        assertNull(ServerConfig.fromPairUri("//pair?v=1&host=x&token=y"))
    }

    @Test
    fun rejectsOpaqueUri() {
        assertNull(ServerConfig.fromPairUri("reeve:pair?v=1&host=x&token=y"))
    }

    @Test
    fun rejectsQueryOnlyUri() {
        assertNull(ServerConfig.fromPairUri("?v=1&host=x&token=y"))
        assertNull(ServerConfig.fromPairUri(""))
    }

    @Test
    fun rejectsWrongSchemeOrVersion() {
        assertNull(ServerConfig.fromPairUri("https://pair?v=1&host=x&token=y"))
        assertNull(ServerConfig.fromPairUri("reeve://pair?v=2&host=x&token=y"))
        assertNull(ServerConfig.fromPairUri("reeve://pair?host=x"))
        assertNull(ServerConfig.fromPairUri("reeve://pair?v=1&host=x"))
        assertNull(ServerConfig.fromPairUri("reeve://other?v=1&host=x&token=y"))
        assertNull(ServerConfig.fromPairUri("not a uri"))
    }
}
