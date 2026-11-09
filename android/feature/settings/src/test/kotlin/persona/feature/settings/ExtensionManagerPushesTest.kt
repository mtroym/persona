package persona.feature.settings

import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.withTimeout
import kotlinx.coroutines.yield
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.ExtensionAuthEvent
import persona.core.contracts.ExtensionUiEvent
import persona.core.contracts.ServerEvent
import persona.core.contracts.api.*

/**
 * The same extension id can be installed under several managers (docs/assistant/managers.md): a sign-in or badge push
 * counts only for the manager that sent it (#1834). The hub names every manager but the assistant it starts.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh")
class ExtensionManagerPushesTest {
    private val res get() = ApplicationProvider.getApplicationContext<android.content.Context>().resources

    private suspend fun until(check: () -> Boolean) = withTimeout(2_000) { while (!check()) yield() }

    private inline fun <reified T : ServerEvent> push(json: String): T = ServerEvent.decode(json) as T

    private fun signedIn(manager: String?, account: String): ExtensionAuthEvent = push(
        """{"type":"extension_auth",${manager?.let { "\"manager\":\"$it\"," } ?: ""}"extension":"example-cis","auth":{"state":"signed_in","account":"$account"}}""")

    // The assistant's copy (the default) and a second manager's, both signed out.
    private val mine = HubExtension("example-cis", "CIS", status = "running", auth = ExtensionAuth("飞书登录", state = "signed_out"), manager = "assistant")
    private val theirs = mine.copy(manager = "m2")

    private class Pushes(vararg listed: HubExtension) : ExtensionApiStub() {
        val pushes = MutableSharedFlow<ExtensionAuthEvent>(extraBufferCapacity = 4)
        val list = listed.toList()
        override val authUpdates: Flow<ExtensionAuthEvent> get() = pushes
        override suspend fun listing() = ExtensionListing(list)
        override suspend fun extension(id: String) = ExtensionDetail(list.first())
    }

    @Test fun theListChangesOnlyTheCopyOfTheManagerThatSignedIn() = runBlocking {
        val api = Pushes(mine, theirs)
        val model = ExtensionListModel(api, res)
        model.load()
        val job = launch(Dispatchers.Unconfined) { model.watchAuth() }
        try {
            api.pushes.emit(signedIn("m2", "them"))
            until { model.state.value.extensions?.any { it.auth?.account == "them" } == true }
            assertEquals(listOf(null, "them"), model.state.value.extensions?.map { it.auth?.account })
            // No manager: the assistant's own copy.
            api.pushes.emit(signedIn(null, "me"))
            until { model.state.value.extensions?.first()?.auth?.account != null }
            assertEquals(listOf("me", "them"), model.state.value.extensions?.map { it.auth?.account })
        } finally { job.cancelAndJoin() }
    }

    @Test fun anExtensionsPageTakesOnlyItsOwnManagersSignIn() = runBlocking {
        val api = Pushes(mine)
        val model = ExtensionModel(api, "example-cis", res)
        model.load()
        val job = launch(Dispatchers.Unconfined) { model.watchAuth() }
        try {
            api.pushes.emit(signedIn("m2", "them"))
            delay(200)
            assertEquals("signed_out", model.state.value.detail?.extension?.auth?.state)
            api.pushes.emit(signedIn(null, "me"))
            until { model.state.value.detail?.extension?.auth?.signedIn == true }
            assertEquals("me", model.state.value.detail?.extension?.auth?.account)
        } finally { job.cancelAndJoin() }
    }

    @Test fun anotherManagersPageTakesThatManagersSignIn() = runBlocking {
        val api = Pushes(theirs)
        val model = ExtensionModel(api, "example-cis", res, manager = "m2")
        model.load()
        val job = launch(Dispatchers.Unconfined) { model.watchAuth() }
        try {
            api.pushes.emit(signedIn(null, "me"))
            delay(200)
            assertEquals("signed_out", model.state.value.detail?.extension?.auth?.state)
            api.pushes.emit(signedIn("m2", "them"))
            until { model.state.value.detail?.extension?.auth?.signedIn == true }
            assertEquals("them", model.state.value.detail?.extension?.auth?.account)
        } finally { job.cancelAndJoin() }
    }

    @Test fun theCatalogPageFollowsOnlyTheDefaultManagersOperations() {
        val api = FakeOperations()
        runBlocking {
            val model = CatalogExtensionModel(api, "m1", "example-notes", res)
            model.load()
            val job = launch(Dispatchers.Unconfined) { model.watch {} }
            try {
                until { "list" in api.calls }
                // The same extension upgrading under a second manager is not this catalog's install.
                api.pushes.emit(op("failed", "download", 2, "download" to "failed", error = "boom").copy(manager = "m2"))
                delay(200)
                assertNull(model.state.value.operation)
                api.pushes.emit(op("running", "download", 3, "download" to "running").copy(manager = ManagerRoster.ASSISTANT_MANAGER))
                until { model.state.value.operation != null }
            } finally { job.cancelAndJoin() }
        }
    }

    @Test fun settingsBadgesAreTheDefaultManagersOnly() = runTest {
        val api = object : ExtensionApiStub() {
            override suspend fun uiPlaces(placement: String, launcher: String?, machine: String?) =
                listOf(ExtensionUiPlace("example-gateway", "usage", "用量", "nav_page"))
        }
        val events = MutableSharedFlow<ExtensionUiEvent>()
        val slots = ExtensionSlots(api, MutableStateFlow(true), events)
        backgroundScope.launch { slots.run(MutableStateFlow(true)) }
        runCurrent()
        // Another manager's copy of the extension: these entries come from the default manager's list.
        events.emit(push("""{"type":"extension_ui","manager":"m2","extension":"example-gateway","id":"usage","badge":{"text":"9"}}"""))
        runCurrent()
        assertNull(slots.state.value.navPages.single().badge)
        events.emit(push("""{"type":"extension_ui","extension":"example-gateway","id":"usage","badge":{"text":"3"}}"""))
        runCurrent()
        assertEquals("3", slots.state.value.navPages.single().badge?.text)
    }
}
