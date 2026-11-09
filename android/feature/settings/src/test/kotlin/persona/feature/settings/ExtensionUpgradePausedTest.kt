package persona.feature.settings

import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.ExtensionDetail
import persona.core.contracts.api.HubExtension
import persona.core.designsystem.PersonaTheme

/**
 * Why an extension's auto-upgrade does not apply now (`auto_upgrade_paused`, docs/extensions/extensions.md): its page says
 * the reason the hub gives, not always "while this extension is off" (#1895: its market was removed).
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h3000dp")
class ExtensionUpgradePausedTest {
    @get:Rule val rule = createComposeRule()

    private fun page(paused: String?) {
        val api = object : ExtensionApiStub() {
            override suspend fun extension(id: String) = ExtensionDetail(HubExtension(id, "Example Notes", "ui", "running",
                autoUpgrade = true, autoUpgradePaused = paused))
        }
        rule.setContent { PersonaTheme(darkTheme = false, reduceMotion = true) { ExtensionPage(api, "example-notes", onBack = {}) } }
        rule.waitForIdle()
    }

    @Test fun aRemovedMarketSaysItNoLongerUpgrades() {
        page("market_removed")
        rule.onNodeWithText("market was removed", substring = true).assertExists()
        rule.onNodeWithText("while this extension is off", substring = true).assertDoesNotExist()
    }

    @Test fun aSwitchedOffExtensionKeepsItsWords() {
        page("disabled")
        rule.onNodeWithText("while this extension is off", substring = true).assertExists()
    }

    @Test fun aReasonThisAppDoesNotKnowIsNotTakenForSwitchedOff() {
        page("some_newer_reason")
        rule.onNodeWithText("while this extension is off", substring = true).assertDoesNotExist()
        rule.onNodeWithText("Auto-upgrade is paused", substring = true).assertExists()
    }
}
