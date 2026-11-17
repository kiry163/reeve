package dev.reeve.app

import dev.reeve.app.core.PairDraft
import dev.reeve.app.core.ServerConfig
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Test

/** 配对草稿 → ServerConfig、配对串 / 深链解析。全是纯函数，不需要 Android。 */
class PairingTest {
    private fun cfg(id: String, url: String, token: String, name: String = "") = ServerConfig(id, name, url, token)

    @Test
    fun trimsDraftAndCompletesScheme() {
        val c = PairDraft("  192.168.1.5:18800  ", "  tok  ").toConfig(emptyList(), newId = "new")
        assertEquals("new", c.id)
        assertEquals("http://192.168.1.5:18800", c.url)
        assertEquals("tok", c.token)
        assertEquals("", c.name)
    }

    @Test
    fun domainDraftDefaultsToHttps() {
        val c = PairDraft("reeve.example.com", "tok").toConfig(emptyList(), newId = "new")
        assertEquals("https://reeve.example.com", c.url)
    }

    @Test
    fun keepsExplicitSchemeAndDropsTrailingSlash() {
        assertEquals("http://host:18800", PairDraft("http://host:18800/", "t").toConfig(emptyList(), "n").url)
        assertEquals("https://host", PairDraft("https://host/", "t").toConfig(emptyList(), "n").url)
    }

    @Test
    fun reusesIdWhenSameHostIsAlreadyKnown() {
        val existing = listOf(cfg("srv-1", "http://192.168.1.5:18800", "old"))
        val c = PairDraft("192.168.1.5:18800", "new-token").toConfig(existing, newId = "fresh")
        assertEquals("srv-1", c.id)
        assertEquals("http://192.168.1.5:18800", c.url)
        assertEquals("new-token", c.token)
    }

    @Test
    fun reusesIdWhenOnlySchemeChanges() {
        val existing = listOf(cfg("srv-1", "http://reeve.example.com:18800", "old"))
        val c = PairDraft("https://reeve.example.com:18800", "new").toConfig(existing, "fresh")
        assertEquals("srv-1", c.id)
        assertEquals("https://reeve.example.com:18800", c.url)
    }

    @Test
    fun keepsExistingNameUnlessDraftNamesIt() {
        val existing = listOf(cfg("srv-1", "http://1.2.3.4:18800", "old", name = "东京 VPS"))
        assertEquals("东京 VPS", PairDraft("1.2.3.4:18800", "new").toConfig(existing, "fresh").name)
        assertEquals("家里", PairDraft("1.2.3.4:18800", "new", name = " 家里 ").toConfig(existing, "fresh").name)
    }

    @Test
    fun differentHostYieldsNewEntry() {
        val existing = listOf(cfg("srv-1", "http://192.168.1.5:18800", "old"))
        val c = PairDraft("192.168.1.6:18800", "new").toConfig(existing, "fresh")
        assertEquals("fresh", c.id)
        assertNotEquals("srv-1", c.id)
        assertEquals("http://192.168.1.6:18800", c.url)
    }

    @Test
    fun differentPortIsDifferentServer() {
        val existing = listOf(cfg("srv-1", "http://192.168.1.5:18800", "old"))
        assertEquals("fresh", PairDraft("192.168.1.5:19000", "new").toConfig(existing, "fresh").id)
    }

    @Test
    fun reusesIdAcrossImplicitDefaultPorts() {
        val existing = listOf(cfg("srv-1", "http://reeve.example.com", "old", name = "东京 VPS"))
        val c = PairDraft("https://reeve.example.com", "new").toConfig(existing, "fresh")
        assertEquals("srv-1", c.id)
        assertEquals("https://reeve.example.com", c.url)
        assertEquals("东京 VPS", c.name)
    }

    @Test
    fun explicitPortStaysDistinctFromImplicitDefaultPort() {
        val existing = listOf(cfg("srv-1", "http://reeve.example.com:18800", "old"))
        assertEquals("fresh", PairDraft("https://reeve.example.com", "new").toConfig(existing, "fresh").id)
        assertEquals("fresh", PairDraft("http://reeve.example.com", "new").toConfig(existing, "fresh").id)
    }

    @Test
    fun editingForcesTargetIdAndName() {
        val target = cfg("srv-A", "http://a.example.com:18800", "old", name = "A")
        val other = cfg("srv-B", "http://b.example.com:18800", "x", name = "B")
        val c = PairDraft("b.example.com:18800", "new").toEditConfig(target, listOf(target, other))
        assertEquals("srv-A", c.id)
        assertEquals("A", c.name)
        assertEquals("https://b.example.com:18800", c.url)
        assertEquals("new", c.token)
    }

    @Test
    fun editingDraftNameOverridesTargetName() {
        val target = cfg("srv-A", "http://a.example.com:18800", "old", name = "A")
        val c = PairDraft("a.example.com:18800", "new", name = " 新名字 ").toEditConfig(target, listOf(target))
        assertEquals("srv-A", c.id)
        assertEquals("新名字", c.name)
    }

    @Test
    fun toStringNeverPrintsToken() {
        val secret = "s3cr3t-token-value"
        assertFalse(cfg("srv-1", "http://1.2.3.4:18800", secret).toString().contains(secret))
        assertFalse(PairDraft("1.2.3.4:18800", secret).toString().contains(secret))
    }

    @Test
    fun rejectsBlankToken() {
        assertThrows(IllegalArgumentException::class.java) { PairDraft("host:18800", "   ").toConfig(emptyList()) }
    }

    @Test
    fun rejectsBlankAddress() {
        assertThrows(IllegalArgumentException::class.java) { PairDraft("   ", "tok").toConfig(emptyList()) }
    }

    @Test
    fun parsesPairStringIntoDraft() {
        val d = PairDraft.fromPairString("reeve://pair?v=1&host=192.168.1.5:18800&token=abc")
        assertEquals("http://192.168.1.5:18800", d?.address)
        assertEquals("abc", d?.token)
        assertEquals("", d?.name)
    }

    @Test
    fun parsesEncodedTokenAndTlsHost() {
        val d = PairDraft.fromPairString("reeve://pair?v=1&host=reeve.example.com&token=a_b-c%3D")
        assertEquals("https://reeve.example.com", d?.address)
        assertEquals("a_b-c=", d?.token)
    }

    @Test
    fun deepLinkDraftSurvivesRoundTrip() {
        val d = PairDraft.fromPairString("reeve://pair?v=1&host=192.168.1.5:18800&token=abc")
        val c = PairDraft(d!!.address, d.token).toConfig(emptyList(), "new")
        assertEquals("http://192.168.1.5:18800", c.url)
        assertEquals("abc", c.token)
    }

    @Test
    fun rejectsInvalidPairStrings() {
        assertNull(PairDraft.fromPairString(null))
        assertNull(PairDraft.fromPairString(""))
        assertNull(PairDraft.fromPairString("https://pair?v=1&host=x&token=y"))
        assertNull(PairDraft.fromPairString("reeve://pair?v=2&host=x&token=y"))
        assertNull(PairDraft.fromPairString("reeve://pair?v=1&host=x"))
        assertNull(PairDraft.fromPairString("pair?v=1&host=x&token=y"))
        assertNull(PairDraft.fromPairString("看了半天也不知道这是什么"))
    }

    @Test
    fun draftFromConfigKeepsEverything() {
        val d = PairDraft.fromConfig(cfg("srv-1", "http://1.2.3.4:18800", "tok", "家里"))
        assertEquals("http://1.2.3.4:18800", d.address)
        assertEquals("tok", d.token)
        assertEquals("家里", d.name)
    }
}
