package dev.reeve.app

import dev.reeve.app.core.Health
import dev.reeve.app.core.PairDraft
import dev.reeve.app.core.PendingPair
import dev.reeve.app.core.canPair
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** 深链来源标记、外部来源的保存门槛、/api/health 的 reeve 校验。 */
class PairSafetyTest {
    private val link = "reeve://pair?v=1&host=10.0.2.2:18880"

    @Test
    fun deepLinkIsMarkedExternal() {
        val pending = PendingPair.fromLink(link)!!
        assertTrue(pending.fromExternalLink)
        assertEquals("http://10.0.2.2:18880", pending.draft.address)
        assertEquals("", pending.draft.password)
    }

    @Test
    fun invalidOrNonPairLinkProducesNoDraft() {
        assertNull(PendingPair.fromLink(null))
        assertNull(PendingPair.fromLink(""))
        assertNull(PendingPair.fromLink("https://bot.example.com/?host=x"))
        assertNull(PendingPair.fromLink("reeve://pair?v=2&host=10.0.2.2:18880"))
        assertNull(PendingPair.fromLink("reeve://pair?v=1&host="))
        assertNull(PendingPair.fromLink("reeve://pair?v=1"))
    }

    @Test
    fun scanPasteAndManualDraftAreNotExternal() {
        val pending = PendingPair.fromInside(PairDraft("host:18800", "pw"))
        assertFalse(pending.fromExternalLink)
        assertEquals("host:18800", pending.draft.address)
    }

    @Test
    fun externalSourceCannotSaveUntilAddressConfirmed() {
        // 来源 × 勾选 × 输入完整性的矩阵：只有外部来源被勾选确认后才放行
        assertFalse(canPair("host:18800", "pw", fromExternalLink = true, addressConfirmed = false))
        assertTrue(canPair("host:18800", "pw", fromExternalLink = true, addressConfirmed = true))
        // 勾选不能绕过空地址 / 空密码
        assertFalse(canPair("", "pw", fromExternalLink = true, addressConfirmed = true))
        assertFalse(canPair("host:18800", "  ", fromExternalLink = true, addressConfirmed = true))
        // 正在登录时也不能重复提交
        assertFalse(canPair("host:18800", "pw", fromExternalLink = true, addressConfirmed = true, busy = true))
    }

    @Test
    fun internalSourceDoesNotNeedConfirmation() {
        assertTrue(canPair("host:18800", "pw", fromExternalLink = false, addressConfirmed = false))
        assertFalse(canPair("", "pw", fromExternalLink = false, addressConfirmed = false))
        assertFalse(canPair("host:18800", "pw", fromExternalLink = false, addressConfirmed = false, busy = true))
    }

    @Test
    fun healthOfAnotherServiceIsRejected() {
        assertTrue(Health(ok = true, version = "0.4.0", apiVersion = 1).looksLikeReeve)
        // 普通 HTTP 服务也可能回 {"ok":true}；没有 api_version 不算 reeve
        assertFalse(Health(ok = true, version = "", apiVersion = 0).looksLikeReeve)
        assertFalse(Health(ok = false, version = "0.4.0", apiVersion = 1).looksLikeReeve)
        assertFalse(Health(ok = true, version = "0.4.0", apiVersion = -1).looksLikeReeve)
    }
}
