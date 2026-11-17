package dev.reeve.app.ui

import dev.reeve.app.core.PairDraft
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** 深链入栈去重：同一 ACTION_VIEW 被重投时不应该再压一层配对页。 */
class PairNavigationTest {
    @Test
    fun opensPairScreenFromAnyOtherScreen() {
        assertTrue(shouldOpenPair(Screen.Home))
        assertTrue(shouldOpenPair(Screen.Settings))
        assertTrue(shouldOpenPair(Screen.Server("srv-1")))
    }

    @Test
    fun doesNotPushPairScreenTwice() {
        assertFalse(shouldOpenPair(Screen.AddServer()))
        assertFalse(shouldOpenPair(Screen.AddServer(existing = "srv-1")))
        assertFalse(shouldOpenPair(Screen.AddServer(draft = PairDraft("host:18800", "pw"))))
    }
}
