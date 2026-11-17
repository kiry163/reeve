package dev.reeve.app

import dev.reeve.app.core.PairDraft
import dev.reeve.app.core.ServerConfig
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Test

/** 配对草稿 → 服务器身份、配对串 / 深链解析。全是纯函数，不需要 Android。 */
class PairingTest {
    private fun cfg(id: String, url: String, token: String, name: String = "") = ServerConfig(id, name, url, token)

    @Test
    fun trimsDraftAndCompletesScheme() {
        val t = PairDraft("  192.168.1.5:18800  ", "  pw  ").toTarget(emptyList(), newId = "new")
        assertEquals("new", t.id)
        assertEquals("http://192.168.1.5:18800", t.url)
        assertEquals("", t.name)
    }

    @Test
    fun domainDraftDefaultsToHttps() {
        val t = PairDraft("reeve.example.com", "pw").toTarget(emptyList(), newId = "new")
        assertEquals("https://reeve.example.com", t.url)
    }

    @Test
    fun keepsExplicitSchemeAndDropsTrailingSlash() {
        assertEquals("http://host:18800", PairDraft("http://host:18800/", "pw").toTarget(emptyList(), "n").url)
        assertEquals("https://host", PairDraft("https://host/", "pw").toTarget(emptyList(), "n").url)
    }

    @Test
    fun reusesIdWhenSameHostIsAlreadyKnown() {
        val existing = listOf(cfg("srv-1", "http://192.168.1.5:18800", "old"))
        val t = PairDraft("192.168.1.5:18800", "new-password").toTarget(existing, newId = "fresh")
        assertEquals("srv-1", t.id)
        assertEquals("http://192.168.1.5:18800", t.url)
    }

    @Test
    fun reusesIdWhenOnlySchemeChanges() {
        val existing = listOf(cfg("srv-1", "http://reeve.example.com:18800", "old"))
        val t = PairDraft("https://reeve.example.com:18800", "pw").toTarget(existing, "fresh")
        assertEquals("srv-1", t.id)
        assertEquals("https://reeve.example.com:18800", t.url)
    }

    @Test
    fun keepsExistingNameUnlessDraftNamesIt() {
        val existing = listOf(cfg("srv-1", "http://1.2.3.4:18800", "old", name = "东京 VPS"))
        assertEquals("东京 VPS", PairDraft("1.2.3.4:18800", "pw").toTarget(existing, "fresh").name)
        assertEquals("家里", PairDraft("1.2.3.4:18800", "pw", name = " 家里 ").toTarget(existing, "fresh").name)
    }

    @Test
    fun differentHostYieldsNewEntry() {
        val existing = listOf(cfg("srv-1", "http://192.168.1.5:18800", "old"))
        val t = PairDraft("192.168.1.6:18800", "pw").toTarget(existing, "fresh")
        assertEquals("fresh", t.id)
        assertNotEquals("srv-1", t.id)
        assertEquals("http://192.168.1.6:18800", t.url)
    }

    @Test
    fun differentPortIsDifferentServer() {
        val existing = listOf(cfg("srv-1", "http://192.168.1.5:18800", "old"))
        assertEquals("fresh", PairDraft("192.168.1.5:19000", "pw").toTarget(existing, "fresh").id)
    }

    @Test
    fun reusesIdAcrossImplicitDefaultPorts() {
        val existing = listOf(cfg("srv-1", "http://reeve.example.com", "old", name = "东京 VPS"))
        val t = PairDraft("https://reeve.example.com", "pw").toTarget(existing, "fresh")
        assertEquals("srv-1", t.id)
        assertEquals("https://reeve.example.com", t.url)
        assertEquals("东京 VPS", t.name)
    }

    @Test
    fun explicitPortStaysDistinctFromImplicitDefaultPort() {
        val existing = listOf(cfg("srv-1", "http://reeve.example.com:18800", "old"))
        assertEquals("fresh", PairDraft("https://reeve.example.com", "pw").toTarget(existing, "fresh").id)
        assertEquals("fresh", PairDraft("http://reeve.example.com", "pw").toTarget(existing, "fresh").id)
    }

    @Test
    fun editingForcesTargetIdAndName() {
        val target = cfg("srv-A", "http://a.example.com:18800", "old", name = "A")
        val other = cfg("srv-B", "http://b.example.com:18800", "x", name = "B")
        val t = PairDraft("b.example.com:18800", "pw").toEditTarget(target, listOf(target, other))
        assertEquals("srv-A", t.id)
        assertEquals("A", t.name)
        assertEquals("https://b.example.com:18800", t.url)
    }

    @Test
    fun editingDraftNameOverridesTargetName() {
        val target = cfg("srv-A", "http://a.example.com:18800", "old", name = "A")
        val t = PairDraft("a.example.com:18800", "pw", name = " 新名字 ").toEditTarget(target, listOf(target))
        assertEquals("srv-A", t.id)
        assertEquals("新名字", t.name)
    }

    @Test
    fun toStringNeverPrintsSecret() {
        val secret = "s3cr3t-session-value"
        assertFalse(cfg("srv-1", "http://1.2.3.4:18800", secret).toString().contains(secret))
        assertFalse(PairDraft("1.2.3.4:18800", "s3cr3t-password").toString().contains("s3cr3t-password"))
    }

    @Test
    fun rejectsBlankAddress() {
        assertThrows(IllegalArgumentException::class.java) { PairDraft("   ", "pw").toTarget(emptyList()) }
    }

    @Test
    fun parsesPairStringIntoDraft() {
        val d = PairDraft.fromPairString("reeve://pair?v=1&host=192.168.1.5:18800")
        assertEquals("http://192.168.1.5:18800", d?.address)
        assertEquals("", d?.password)
        assertEquals("", d?.name)
    }

    @Test
    fun ignoresLegacyTokenInPairString() {
        val d = PairDraft.fromPairString("reeve://pair?v=1&host=192.168.1.5:18800&token=abc")
        assertEquals("http://192.168.1.5:18800", d?.address)
        assertEquals("", d?.password)
    }

    @Test
    fun deepLinkDraftSurvivesRoundTrip() {
        val d = PairDraft.fromPairString("reeve://pair?v=1&host=192.168.1.5:18800")
        val t = PairDraft(d!!.address, "pw").toTarget(emptyList(), "new")
        assertEquals("http://192.168.1.5:18800", t.url)
    }

    @Test
    fun rejectsInvalidPairStrings() {
        assertNull(PairDraft.fromPairString(null))
        assertNull(PairDraft.fromPairString(""))
        assertNull(PairDraft.fromPairString("https://pair?v=1&host=x"))
        assertNull(PairDraft.fromPairString("reeve://pair?v=2&host=x"))
        assertNull(PairDraft.fromPairString("reeve://pair?v=1&host="))
        assertNull(PairDraft.fromPairString("pair?v=1&host=x"))
        assertNull(PairDraft.fromPairString("看了半天也不知道这是什么"))
    }

    @Test
    fun draftFromConfigKeepsAddressAndNameButNotSessionToken() {
        val d = PairDraft.fromConfig(cfg("srv-1", "http://1.2.3.4:18800", "tok", "家里"))
        assertEquals("http://1.2.3.4:18800", d.address)
        assertEquals("", d.password)
        assertEquals("家里", d.name)
    }
}
