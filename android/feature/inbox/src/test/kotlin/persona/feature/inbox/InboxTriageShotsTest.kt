package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File

/**
 * The inbox while new items wait for triage (#1773), on the JVM with Robolectric's native graphics (no emulator), in
 * every style, light and dark: a model not set up (with "Set up"), the model behind, and several sources. The banner sits
 * under the filters, over the first item. Each is checked to draw something and written as a PNG to
 * `build/inbox-triage-shots/` (or `-Dpersona.shots=<dir>`) to look at (docs/android/android.md "Inbox").
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class InboxTriageShotsTest {
    @get:Rule val rule = createComposeRule()

    private val records = listOf(
        InboxRecord(InboxItem("mail:a1:t:7", kind = "mail", title = "周报", sender = "示例同事", preview = "这周的进展和下周的计划，见附件。",
            tier = "action", updatedAt = "2026-11-04T09:30:00Z"), sourceLabel = "邮件", source = "mail"),
        InboxRecord(InboxItem("lark:f2:120", kind = "group", title = "项目群", sender = "示例同事", preview = "评审改到周四下午了",
            tier = "fyi", updatedAt = "2026-11-04T08:10:00Z"), sourceLabel = "飞书", feedId = "f2", count = 2, source = "lark"),
    )

    private enum class Shot(val triage: InboxTriage) {
        Unconfigured(InboxTriage("unconfigured", null, 3, listOf(InboxTriageSource("lark", "飞书", "unconfigured", waiting = 3)))),
        Behind(InboxTriage("behind", null, 12, listOf(InboxTriageSource("lark", "飞书", "behind", waiting = 12)))),
        Several(InboxTriage("error", "模型没有额度", 5, listOf(InboxTriageSource("lark", "飞书", "error", "模型没有额度", 3),
            InboxTriageSource("examplechat", "示例聊天", "timeout", waiting = 2)))),
    }

    private class OneInbox(snapshot: InboxSnapshot) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(snapshot)
        override val connectionState = flowOf(ConnectionState.Online)
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/inbox-triage-shots").also { it.mkdirs() }

    private fun capture(name: String) {
        rule.waitForIdle()
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val background = bitmap.getPixel(1, 1)
        var other = 0
        for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) if (bitmap.getPixel(x, y) != background) other++
        assertTrue("$name drew nothing", other > 100)
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    @Test fun drawsTheBannerInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var shot by mutableStateOf(Shot.Unconfigured)
        var run by mutableIntStateOf(0)
        var configured = 0
        val setup = InboxTriageSetup(mayConfigure = { true }, configure = { configured++ })
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                key(run) {
                    CompositionLocalProvider(LocalInboxTriageSetup provides setup) {
                        Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                            InboxScreen(OneInbox(InboxSnapshot(records = records, triage = shot.triage)), navigator)
                        }
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) for (next in Shot.entries) {
            style = s; dark = d; shot = next; run++
            rule.waitForIdle()
            rule.onNodeWithText(InboxTriageText.banner(androidx.test.core.app.ApplicationProvider.getApplicationContext<android.content.Context>().resources, next.triage)!!.text).assertExists()
            val offered = rule.onAllNodes(hasText("去配置") and hasClickAction()).fetchSemanticsNodes().size
            assertEquals("$next: 去配置 only for a model not set up", if (next == Shot.Unconfigured) 1 else 0, offered)
            capture("inbox-triage-${next.name.lowercase()}-${s.name.lowercase()}-${if (d) "dark" else "light"}")
        }
        shot = Shot.Unconfigured; run++
        rule.waitForIdle()
        rule.onNode(hasText("去配置") and hasClickAction()).performClick()
        assertEquals(1, configured)
    }

    // A device that may not change settings (it may only read and answer) reads the same words without "Set up"; triage
    // back to ok, or an older hub: no banner at all.
    @Test fun textOnlyWhereTheDeviceMayNotConfigureAndNothingWhenOk() {
        var triage by mutableStateOf<InboxTriage?>(Shot.Unconfigured.triage)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                CompositionLocalProvider(LocalInboxTriageSetup provides InboxTriageSetup(mayConfigure = { false }, configure = {})) {
                    key(triage) { InboxScreen(OneInbox(InboxSnapshot(records = records, triage = triage)), navigator) }
                }
            }
        }
        rule.waitForIdle()
        rule.onNodeWithText("有 3 条飞书消息在等分拣：还没配分拣模型").assertExists()
        assertEquals(0, rule.onAllNodes(hasText("去配置")).fetchSemanticsNodes().size)
        triage = InboxTriage("ok", null, 0)
        rule.waitForIdle()
        assertEquals(0, rule.onAllNodes(hasText("在等分拣", substring = true)).fetchSemanticsNodes().size)
        triage = null
        rule.waitForIdle()
        assertEquals(0, rule.onAllNodes(hasText("在等分拣", substring = true)).fetchSemanticsNodes().size)
    }

    // #1849: a source the hub could not read this round keeps its earlier items, and the list says so (the hub's
    // read_reason, a plain line without one) apart from the triage banner, until the next good read.
    @Test fun saysWhichSourcesWereNotReadThisRound() {
        fun triage(json: String) = InboxTriage.decode(persona.core.contracts.JsonCodec.parseToJsonElement(json))
        val unread = triage("""{"state":"ok","waiting":0,"sources":[
            {"source":"examplechat","name":"示例聊天","state":"ok","read_error":"timeout","read_reason":"这一轮没读到：超时了"},
            {"source":"lark","name":"飞书","state":"ok","read_error":"failed"},
            {"source":"mail","name":"邮件","state":"ok"}]}""")
        var shown by mutableStateOf(unread)
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                key(shown, style, dark) {
                    Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                        InboxScreen(OneInbox(InboxSnapshot(records = records, triage = shown)), navigator)
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            rule.onNodeWithText("示例聊天：这一轮没读到：超时了", substring = true).assertExists()
            rule.onNodeWithText("飞书：这一轮没读到，列表可能不全", substring = true).assertExists()
            assertEquals("a source that was read says nothing", 0, rule.onAllNodes(hasText("邮件：", substring = true)).fetchSemanticsNodes().size)
            capture("inbox-unread-${s.name.lowercase()}-${if (d) "dark" else "light"}")
        }
        // Read again: the notice goes.
        shown = triage("""{"state":"ok","waiting":0,"sources":[{"source":"examplechat","name":"示例聊天","state":"ok"}]}""")
        rule.waitForIdle()
        assertEquals(0, rule.onAllNodes(hasText("这一轮没读到", substring = true)).fetchSemanticsNodes().size)
    }
}
