package persona.feature.inbox

import android.content.res.Resources
import androidx.compose.ui.res.stringResource

import androidx.compose.foundation.layout.padding
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.produceState
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Modifier
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import persona.core.contracts.JsonCodec
import persona.core.designsystem.JNotice
import persona.core.designsystem.Status
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PhIcons

// New items waiting for triage (#1773): since #1644 an extension's new items enter the inbox only once the model has
// sorted them, and the hub says in GET /api/v1/inbox `triage` how many wait and why (server/internal/hub/
// inbox_admit.go). While some wait, a banner over the list says so: the same words, place and tone on the web, iOS
// and Android.

/** How triage stands; null from an older hub, which sends none. */
@Serializable
data class InboxTriage(
    /** ok | unconfigured | paused | error | timeout | behind. */
    val state: String = "",
    /** The state for people, the hub's own words; may be empty. */
    val reason: String? = null,
    val waiting: Int = 0,
    val sources: List<InboxTriageSource> = emptyList(),
) {
    companion object {
        /** The model use "Set up" opens: "Inbox triage", the one that sorts the inbox (Settings › Models). */
        const val Purpose = "inbox.triage"

        /** The response's `triage`; anything else (missing, not an object, unreadable) is none. */
        fun decode(json: JsonElement?): InboxTriage? = (json as? JsonObject)?.let {
            runCatching { JsonCodec.decodeFromJsonElement(serializer(), it) }.getOrNull()
        }
    }
}

/** One source's share of the waiting items. */
@Serializable
data class InboxTriageSource(
    val source: String = "",
    val name: String? = null,
    val state: String = "",
    val reason: String? = null,
    val waiting: Int = 0,
    /** timeout | rate_limited | failed: the hub could not read it this round, so its items are the last round's. */
    @SerialName("read_error") val readError: String? = null,
    /** That, for people, in the hub's words. */
    @SerialName("read_reason") val readReason: String? = null,
)

/** The banner's words, and whether it offers "Set up" (the model is not set up). */
data class InboxTriageBanner(val text: String, val configure: Boolean)

internal object InboxTriageText {
    /** What a state says when the hub gives no reason (the same words on the web and iOS). */
    val reasons = mapOf(
        "unconfigured" to R.string.inbox_triage_unconfigured,
        "paused" to R.string.inbox_triage_paused,
        "error" to R.string.inbox_triage_error,
        "timeout" to R.string.inbox_triage_timeout,
        "behind" to R.string.inbox_triage_behind,
    )

    /**
     * The banner while new items wait; null when none wait, triage is ok, or the hub is older. One source waiting
     * names it ("3 Lark messages waiting for triage: …"), several do not; the reason is the hub's, else the state's words (an
     * unknown state without a reason says only how many wait).
     */
    fun banner(res: Resources, triage: InboxTriage?): InboxTriageBanner? {
        if (triage == null || triage.state.isEmpty() || triage.state == "ok" || triage.waiting <= 0) return null
        val waiting = triage.sources.filter { it.waiting > 0 }
        val name = waiting.singleOrNull()?.name?.trim().orEmpty()
        val reason = triage.reason?.trim()?.takeIf { it.isNotEmpty() } ?: reasons[triage.state]?.let(res::getString).orEmpty()
        val waitingText = if (name.isEmpty()) res.getQuantityString(R.plurals.inbox_triage_waiting, triage.waiting, triage.waiting)
            else res.getQuantityString(R.plurals.inbox_triage_waiting_source, triage.waiting, triage.waiting, name)
        return InboxTriageBanner(if (reason.isNotEmpty()) res.getString(R.string.inbox_triage_with_reason, waitingText, reason) else waitingText,
            configure = triage.state == "unconfigured")
    }

    /**
     * The sources the hub could not read this round (#1849, `read_error`): their items are the last round's, so the list
     * may be behind. One line each, "name: the hub's read_reason" (a plain one without), apart from the banner and its
     * waiting count, which a failed read does not change; null once every source was read, or from an older hub.
     */
    fun unread(res: Resources, triage: InboxTriage?): String? = triage?.sources.orEmpty()
        .filter { !it.readError.isNullOrBlank() }
        .map { source ->
            val name = source.name?.trim()?.takeIf { it.isNotEmpty() } ?: source.source
            val reason = source.readReason?.trim()?.takeIf { it.isNotEmpty() } ?: res.getString(R.string.inbox_read_failed)
            res.getString(R.string.inbox_triage_with_reason, name, reason)
        }
        .takeIf { it.isNotEmpty() }?.joinToString("\n")
}

/**
 * Where "Set up" goes (Settings › Models › Inbox triage) and whether this device may change it ([mayConfigure]: a device that may
 * only read and answer may not, nor a hub without that model use). The shell provides it; none: text only.
 */
class InboxTriageSetup(val mayConfigure: suspend () -> Boolean, val configure: () -> Unit)

val LocalInboxTriageSetup = staticCompositionLocalOf<InboxTriageSetup?> { null }

/** The quiet line over the list naming the sources not read this round ([InboxTriageText.unread]); not dismissable. */
@Composable
internal fun InboxUnreadNotice(text: String, modifier: Modifier = Modifier.padding(top = Tokens.Space.s)) {
    JNotice(text, modifier, status = Status.Unknown, icon = PhIcons.Info)
}

/**
 * The banner over the list while new items wait for triage: the design system's notice in the warning tone, not
 * dismissable, gone once triage is ok. "Set up" only when the model is not set up and this device may set it up.
 */
@Composable
internal fun InboxTriageNotice(banner: InboxTriageBanner, modifier: Modifier = Modifier.padding(top = Tokens.Space.s)) {
    val setup = LocalInboxTriageSetup.current
    val allowed by produceState(false, setup, banner.configure) {
        value = banner.configure && setup != null && runCatching { setup.mayConfigure() }.getOrDefault(false)
    }
    JNotice(banner.text, modifier, status = Status.NeedsYou, icon = PhIcons.Clock,
        actionLabel = stringResource(R.string.inbox_triage_configure).takeIf { allowed }, onAction = setup?.configure?.takeIf { allowed })
}
