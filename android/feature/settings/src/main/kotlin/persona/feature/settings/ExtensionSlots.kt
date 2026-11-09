package persona.feature.settings

import androidx.compose.ui.res.stringResource
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.style.TextOverflow
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.repeatOnLifecycle
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.GroupKeys
import persona.core.contracts.ExtensionUiEvent
import persona.core.contracts.pushManager
import persona.core.contracts.api.ExtensionManagement
import persona.core.contracts.api.ExtensionUi
import persona.core.contracts.api.ExtensionUiContext
import persona.core.contracts.api.ExtensionUiPlace
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.filter
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

// Extensions' UI slots (docs/extensions/extensions.md "UI slots") on Android: the "Extensions" section at the top of the settings home (the
// cards first, then a row per nav page with its icon and badge), the settings tab's badge (the nav pages' badges added
// up), the sections extensions draw on a machine's page, and the pages they draw for their connectors' inbox items
// (ExtensionInboxItem.kt). A hub without extension_ui_slots has none of them.

/** The slots this app draws; a placement it does not know is ignored. */
internal object ExtensionSlotText {
    const val NavPage = "nav_page"
    const val Card = "overview_card"
    const val MachineSection = "machine_section"
    const val InboxItem = "inbox_item"
    private val known = setOf(NavPage, Card, MachineSection)

    fun known(place: ExtensionUiPlace): Boolean = place.placement in known && place.extension.isNotEmpty() && place.id.isNotEmpty()

    /** An inbox item's page: for the items of one connector; one that names none draws for nobody's. */
    fun inboxItem(place: ExtensionUiPlace): Boolean =
        place.placement == InboxItem && place.extension.isNotEmpty() && place.id.isNotEmpty() && !place.connector.isNullOrBlank()

    /** A nav page's icon from the few an extension may name; anything else is the extension's puzzle piece. */
    fun icon(name: String?): PhIcon = when (name) {
        "chart" -> PhIcons.ChartBar
        "gauge" -> PhIcons.Gauge
        "list" -> PhIcons.ListChecks
        "bell" -> PhIcons.BellSimple
        "doc" -> PhIcons.FileText
        "calendar" -> PhIcons.CalendarBlank
        "money" -> PhIcons.Coins
        "branch", "merge" -> PhIcons.GitBranch
        "code" -> PhIcons.Code
        "pull_request" -> PhIcons.GitDiff
        else -> PhIcons.PuzzlePiece
    }

    /**
     * The settings tab's badge from the nav pages': the numbers added up; when there are only words ("New"), a dot.
     */
    fun tabBadge(places: List<ExtensionUiPlace>): SlotBadge {
        val texts = places.filter { it.placement == NavPage }.mapNotNull { it.badge?.text?.trim()?.takeIf(String::isNotEmpty) }
        val count = texts.sumOf { it.toIntOrNull()?.coerceAtLeast(0) ?: 0 }
        return SlotBadge(count, dot = count == 0 && texts.any { it.toIntOrNull() == null })
    }

    /** A page's title for the row that opens it: the card's `opens` names a page of the same extension. */
    fun opened(places: List<ExtensionUiPlace>, card: ExtensionUiPlace): ExtensionUiPlace? {
        val id = card.opens?.takeIf { it.isNotEmpty() } ?: return null
        return places.firstOrNull { it.extension == card.extension && it.id == id }
            ?: ExtensionUiPlace(extension = card.extension, id = id, title = card.title, placement = NavPage)
    }
}

/** A nav page's icon, for the app's rail (#2951). */
fun extensionNavIcon(name: String?): PhIcon = ExtensionSlotText.icon(name)

/** A badge for the settings tab: a count, or a dot when the extensions' badges are words. */
data class SlotBadge(val count: Int = 0, val dot: Boolean = false)

data class ExtensionSlotsState(
    /** The hub has UI slots (`extension_ui_slots`) and the list has been read at least once. */
    val available: Boolean = false,
    val places: List<ExtensionUiPlace> = emptyList(),
    val error: ExtensionFailure? = null,
    /** The pages extensions draw for their connectors' inbox items (`inbox_item`); none on a hub that lists none. */
    val inboxItems: List<ExtensionUiPlace> = emptyList(),
    /** [inboxItems] has been read from this hub; until then an item opened (at launch) may turn out to be an extension's. */
    val inboxItemsKnown: Boolean = false,
) {
    val navPages: List<ExtensionUiPlace> get() = places.filter { it.placement == ExtensionSlotText.NavPage }
    val cards: List<ExtensionUiPlace> get() = places.filter { it.placement == ExtensionSlotText.Card }
    val machineSections: List<ExtensionUiPlace> get() = places.filter { it.placement == ExtensionSlotText.MachineSection }
    val tabBadge: SlotBadge get() = ExtensionSlotText.tabBadge(places)
}

/**
 * Where the selected hub's extensions draw in this app, kept for the whole shell: read when the hub (with
 * `extension_ui_slots`) is online and when the settings list shows, and the nav pages' badges changed by the hub's
 * `extension_ui` events without reading the list again.
 */
class ExtensionSlots internal constructor(
    internal val api: ExtensionUi,
    private val slotsAvailable: Flow<Boolean>,
    private val events: Flow<ExtensionUiEvent>,
) {
    constructor(extensions: ExtensionManagement) : this(extensions, extensions.slotsAvailable, extensions.uiEvents)

    private val mutable = MutableStateFlow(ExtensionSlotsState())
    val state: StateFlow<ExtensionSlotsState> = mutable.asStateFlow()
    private var supported = false

    /** Runs while the shell does: reads the slots whenever the hub says it has them and is online, applies badges. */
    suspend fun run(online: Flow<Boolean>) = coroutineScope {
        launch { events.collect(::badge) }
        combine(slotsAvailable, online) { slots, up -> slots to up }.distinctUntilChanged().collectLatest { (slots, up) ->
            supported = slots
            if (!slots) mutable.value = ExtensionSlotsState()
            else if (up) load()
        }
    }

    /**
     * Reads the list again (the settings list shows, the hub reconnected), then the inbox items' pages, which the hub
     * lists by their placement only; a failure keeps what was there.
     */
    suspend fun load() {
        if (!supported) return
        try {
            val places = api.uiPlaces("").filter(ExtensionSlotText::known)
            mutable.update { it.copy(available = true, places = places, error = null) }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) { mutable.update { it.copy(error = ExtensionFailure(R.string.settings_ext_slots_failed, failureReasonText(error))) } }
        loadInboxItems()
    }

    /**
     * The `inbox_item` pages (#1683), read with the other slots and again whenever the inbox shows. A hub that does not
     * know the slot lists none; one that cannot answer keeps the pages known so far, and an item whose page cannot be
     * drawn opens its own page instead (rememberInboxItemBody).
     */
    suspend fun loadInboxItems() {
        if (!supported) return
        try {
            val pages = api.uiPlaces(ExtensionSlotText.InboxItem).filter(ExtensionSlotText::inboxItem)
            mutable.update { it.copy(inboxItems = pages, inboxItemsKnown = true) }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) { /* Nothing to tell: these items open their own pages. */ }
    }

    /** The `ui.invalidate`s among the hub's events (UI version 3): the views on screen are drawn again. */
    val invalidations: Flow<ExtensionUiEvent> get() = events.filter { it.invalidate }

    /**
     * An `extension_ui` event: the nav page's badge changes (none clears it); an invalidation leaves badges alone. These
     * entries are the default manager's list, so another manager's badge for its own copy is left out (#1834).
     */
    fun badge(event: ExtensionUiEvent) = if (event.invalidate || pushManager(event.manager) != ExtensionOwners.default) Unit else mutable.update { s ->
        s.copy(places = s.places.map { if (it.extension == event.extension && it.id == event.id && it.placement == ExtensionSlotText.NavPage) it.copy(badge = event.badge?.takeIf { b -> b.text.isNotBlank() }) else it })
    }
}

/** The shell's slots for the selected hub's extensions, running while the shell is composed; null in demo mode. */
@Composable
fun rememberExtensionSlots(extensions: ExtensionManagement?, connection: StateFlow<ConnectionState>): ExtensionSlots? {
    val slots = remember(extensions) { extensions?.let(::ExtensionSlots) } ?: return null
    LaunchedEffect(slots, connection) { slots.run(connection.map { it == ConnectionState.Online }) }
    return slots
}

/**
 * Runs [model]'s `refresh_after` while the page is resumed (not in the background, not left for another page), and
 * follows [events]' `ui.invalidate` for it: drawn at once while resumed, else when it resumes.
 */
@Composable
internal fun KeepFresh(model: ExtensionViewModel, events: Flow<ExtensionUiEvent>? = null) {
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    LaunchedEffect(model, lifecycle) {
        lifecycle.repeatOnLifecycle(Lifecycle.State.RESUMED) {
            model.shown(true)
            try { model.keepFresh() } finally { model.shown(false) }
        }
    }
    if (events != null) LaunchedEffect(model, events) {
        events.filter { ExtensionViewText.invalidates(it, model.extension, model.ui) }.collect { model.invalidate() }
    }
}

/**
 * The "Extensions" section at the top of the settings home: each card an extension draws (`overview_card`, compact; tapping it
 * opens the page it names), then a row per nav page with its icon and badge. Nothing when no extension draws there.
 */
internal fun LazyListScope.extensionSlotItems(
    state: ExtensionSlotsState,
    cards: List<ExtensionCardSlot>,
    selected: (ExtensionUiPlace) -> Boolean,
    onOpen: (ExtensionUiPlace) -> Unit,
) {
    val pages = state.navPages
    val shown = cards.filter { !it.state.unsupported }
    if (pages.isEmpty() && shown.isEmpty()) return
    item("slots-title") { JSectionHeader(stringResource(R.string.settings_ext_title)) }
    shown.forEach { card ->
        item("slot-card-${card.place.extension}-${card.place.id}") {
            val opens = ExtensionSlotText.opened(state.places, card.place)
            ExtensionCard(card.place, card.state, card.handle, onOpen = opens?.let { { onOpen(it) } }, onRetry = card.reload)
        }
    }
    pages.forEachIndexed { index, page ->
        item("slot-page-${page.extension}-${page.id}") {
            JGroupItem(GroupPosition.of(index, pages.size), if (index == 0 && shown.isNotEmpty()) Modifier.padding(top = Tokens.Space.xs) else Modifier) {
                val badge = page.badge?.takeIf { it.text.isNotBlank() }
                JRow(page.title.ifBlank { page.id }, leadingIcon = ExtensionSlotText.icon(page.icon), selected = selected(page),
                    subtitle = page.extensionName?.takeIf { it.isNotBlank() && it != page.title },
                    trailing = badge?.let { b -> { JTag(b.text, ExtensionViewText.tone(b.tone)) } },
                    chevron = true, onClick = { onOpen(page) })
            }
        }
    }
}

/** A card an extension draws: its title, the compact view, and a chevron when tapping it opens a page. */
@Composable
private fun ExtensionCard(place: ExtensionUiPlace, state: ExtensionViewState, handle: ExtensionViewHandle, onOpen: (() -> Unit)?, onRetry: () -> Unit) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    JCard(Modifier.padding(bottom = Tokens.Space.m), onClick = onOpen) {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Column(Modifier.weight(1f)) {
                    Text(state.view?.title?.takeIf { it.isNotBlank() } ?: place.title.ifBlank { place.id }, style = type.headline, color = palette.ink,
                        maxLines = 1, overflow = TextOverflow.Ellipsis)
                    place.extensionName?.takeIf { it.isNotBlank() }?.let { Text(it, style = type.caption, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis) }
                }
                if (state.loading && state.view != null) { Spacer(Modifier.width(Tokens.Space.s)); JSpinner() }
                if (onOpen != null) { Spacer(Modifier.width(Tokens.Space.s)); JIcon(PhIcons.CaretRight, null, size = Tokens.Space.l, tint = palette.ink3) }
            }
            when {
                state.view != null -> ExtensionCardContent(state, handle)
                state.error != null -> Row(verticalAlignment = Alignment.CenterVertically) {
                    JInlineStatus(state.error.text(), Modifier.weight(1f), tone = InlineTone.Failed)
                    Spacer(Modifier.width(Tokens.Space.s))
                    JButton(stringResource(R.string.settings_ext_retry), onRetry, kind = JButtonKind.Secondary, size = JButtonSize.Small, enabled = !state.loading)
                }
                else -> Text(stringResource(R.string.settings_ext_loading), style = type.caption, color = palette.ink3, maxLines = 1)
            }
        }
    }
}

/** A card of the settings home as drawn now: where it is, its view, its buttons, and reading it again ("Retry"). */
internal class ExtensionCardSlot(val place: ExtensionUiPlace, val state: ExtensionViewState, val handle: ExtensionViewHandle, val reload: () -> Unit)

/** The cards of the settings home, each drawn compact and kept fresh while settings is on screen. */
@Composable
internal fun rememberExtensionCards(slots: ExtensionSlots?, onOpen: (persona.core.navigation.ExtensionUiRoute) -> Unit = {}): List<ExtensionCardSlot> {
    slots ?: return emptyList()
    val state by slots.state.collectAsState()
    val cards = state.cards
    val models = remember(slots, cards.map { it.extension to it.id }) {
        cards.map { it to ExtensionViewModel(slots.api, it, ExtensionUiContext(size = "compact")) }
    }
    val scope = rememberCoroutineScope()
    return models.map { (place, model) ->
        key(place.extension, place.id) {
            LaunchedEffect(model) { model.load() }
            KeepFresh(model, slots.invalidations)
            ExtensionCardSlot(place, model.state.collectAsState().value,
                rememberExtensionView(model, onPush = { onOpen(ExtensionViewText.opened(place, it)) })) { scope.launch { model.load() } }
        }
    }
}

/**
 * The sections extensions draw on machine [machineId]'s page (`machine_section`, after the machine's own), as a lazy
 * list's items: each with its title, kept fresh while the page is resumed. An extension whose hub does not know the
 * machine (one of another hub) draws nothing; a failure shows with "Retry".
 */
@Composable
fun rememberMachineExtensionSections(slots: ExtensionSlots?, machineId: String,
                                     onOpen: (persona.core.navigation.ExtensionUiRoute) -> Unit = {}): LazyListScope.() -> Unit {
    slots ?: return {}
    val state by slots.state.collectAsState()
    val places = state.machineSections
    // The hub knows its machines by their own ids (no group suffix).
    val context = remember(machineId) { ExtensionUiContext(machine = GroupKeys.raw(machineId), size = "full") }
    val models = remember(slots, context, places.map { it.extension to it.id }) {
        places.map { it to ExtensionViewModel(slots.api, it, context) }
    }
    val scope = rememberCoroutineScope()
    val sections = models.map { (place, model) ->
        key(place.extension, place.id) {
            LaunchedEffect(model) { model.load() }
            KeepFresh(model, slots.invalidations)
            Triple(place to model, model.state.collectAsState().value,
                rememberExtensionView(model, onPush = { onOpen(ExtensionViewText.opened(place, it)) }))
        }
    }
    return {
        sections.forEach { (pair, view, handle) ->
            val (place, model) = pair
            if (view.unsupported || (view.view == null && view.error == null)) return@forEach
            val key = "plugin-${place.extension}-${place.id}"
            item("$key-title") { JSectionHeader(view.view?.title?.takeIf { it.isNotBlank() } ?: place.title.ifBlank { place.id }) }
            if (view.view == null && view.error != null) item("$key-error") {
                JNotice(view.error.text(), Modifier.padding(top = Tokens.Space.xs), status = Status.NeedsYou, actionLabel = stringResource(R.string.settings_ext_retry),
                    onAction = { scope.launch { model.load() } })
            } else if (view.error != null) item("$key-error") {
                JInlineStatus(view.error.text(), Modifier.padding(vertical = Tokens.Space.xs), tone = InlineTone.Failed)

            }
            extensionViewItems(key, view, handle, showTitles = true)
        }
    }
}
