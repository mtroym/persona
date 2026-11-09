package persona.feature.inbox

import persona.core.contracts.api.InboxBulkAction
import persona.core.designsystem.text
import androidx.compose.ui.platform.LocalResources
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveableStateHolder
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import android.content.res.Resources
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import persona.core.contracts.api.ConnectionState
import persona.core.domain.inbox.*
import persona.core.navigation.ChatRoute
import persona.core.navigation.ConnectionSettingsRoute
import persona.core.navigation.Navigator
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch

/** [onOpenCard]: the app opens a request card outside any session on Agents (#5610); without it, Agents opens. */
@Composable
fun InboxScreen(repository: InboxRepository, navigator: Navigator, onOpenCard: ((persona.core.navigation.RequestRef) -> Unit)? = null) {
    val snapshot by repository.snapshot.collectAsStateWithLifecycle()
    val connection by repository.connectionState.collectAsStateWithLifecycle(ConnectionState.Connecting)
    val onlineSince = rememberOnlineSince(connection)
    val shownConnection = rememberSettledConnection(connection)
    val savedPreferences by repository.preferences.collectAsStateWithLifecycle(InboxPreferences())
    val feedback = persona.core.designsystem.LocalAppFeedback.current
    val res = LocalResources.current
    val scope = rememberCoroutineScope()
    val composers = rememberReplyComposers(repository)
    val conversations by repository.conversationInbox.collectAsStateWithLifecycle()
    val conversationList by (conversations?.state ?: remember { kotlinx.coroutines.flow.MutableStateFlow(InboxConversationList()) })
        .collectAsStateWithLifecycle()
    var filters by remember(repository) { mutableStateOf<InboxPreferences?>(null) }
    val currentFilters = filters ?: savedPreferences
    val query = currentFilters.query
    val source = currentFilters.source
    val kind = currentFilters.kind
    val section = currentFilters.section
    val expanded = currentFilters.expanded
    var preferenceError by remember { mutableStateOf(false) }
    val preferenceWrites = remember(repository) { kotlinx.coroutines.sync.Mutex() }
    fun filter(value: InboxPreferences) {
        filters = value
        scope.launch(start = kotlinx.coroutines.CoroutineStart.UNDISPATCHED) {
            kotlinx.coroutines.withContext(kotlinx.coroutines.NonCancellable) {
                preferenceWrites.lock()
                try { repository.savePreferences(value); preferenceError = false }
                catch (_: Exception) { preferenceError = true }
                finally { preferenceWrites.unlock() }
            }
        }
    }
    val listStates = rememberSaveableStateHolder()
    var error by remember { mutableStateOf<String?>(null) }
    var refreshing by remember { mutableStateOf(false) }
    val pending = remember { mutableStateMapOf<String, Boolean>() }
    val roster = LocalManagerRoster.current
    val records = snapshot.records.associateBy { it.item.id }
    val interactions = snapshot.interactions.associateBy { it.id }
    val items = orderHubInbox(snapshot.records.map { record ->
        pending[record.item.id]?.let { done ->
            record.item.copy(doneAt = if (done) java.time.Instant.now().toString() else null)
        } ?: record.item
    })
    fun list(settings: InboxListSettings): InboxConversationView {
        val original = buildInboxList(
            items = items, interactions = snapshot.interactions, sessions = snapshot.sessions,
            mutes = snapshot.mutes, settings = settings,
        )
        return conversationInboxView(original, if (conversations == null) null else conversationList, settings, pending,
            persona.core.contracts.CoreText.plain(res.getString(R.string.inbox_conversations_read_pending)))
    }
    val view = list(InboxListSettings(source, kind, query, expanded.toSet() + section))
    val result = view.list
    val conversationRows = view.conversations.associateBy { it.itemId }
    // In progress: the hub says so (`status`, inbox phase 1), or, from an older hub, the AI twin is drafting a reply.
    fun working(row: InboxRow) = row.type == InboxRowType.Item && records[row.id]?.agentWorking == true
    // A hub that sends `status` calls the section In progress; an older hub's is the AI twin's "Persona is working on it" as before.
    val eventHub = snapshot.records.any { it.item.status != null }
    // The summary speaks for the whole inbox, so it reads the unfiltered list.
    val overall = if (query.isEmpty() && source.isEmpty() && kind.isEmpty()) result else list(InboxListSettings()).list
    val workingRows = result.groups.filter { it.id != "done" }.flatMap { it.rows }.filter(::working)
    val triageGroups = result.groups.mapNotNull { group ->
        if (group.id == "done") group else {
            val rows = group.rows.filterNot(::working)
            if (rows.isEmpty()) null else group.copy(rows = rows, visibleRows = group.visibleRows.filterNot(::working))
        }
    }
    val displayGroups = buildList {
        triageGroups.firstOrNull { it.id == "action" }?.let { add(it) }
        if (workingRows.isNotEmpty()) add(InboxGroup("working", persona.core.contracts.CoreText.plain(res.getString(
            if (eventHub) R.string.inbox_section_working else R.string.inbox_group_working)), workingRows, workingRows, false))
        addAll(triageGroups.filter { it.id != "action" })
    }
    val groups = displayGroups.filter { section.isEmpty() || it.id == section }
    val needsCount = needsYouCount(result, ::working)
    val doneCount = result.groups.firstOrNull { it.id == "done" }?.count ?: 0
    val allCount = result.groups.sumOf { it.count }
    val sources = listOf("" to res.getString(R.string.inbox_all_sources)) + snapshot.records.map { record ->
        record.item.account to record.sourceLabel.ifBlank { if (record.feedId != null) res.getString(R.string.inbox_source_lark) else record.item.account.ifBlank { res.getString(R.string.inbox_source_mail) } }
    }.filter { it.first.isNotEmpty() }.distinctBy { it.first }
    var showFilters by remember { mutableStateOf(false) }
    var filterPage by remember { mutableStateOf<String?>(null) }
    var handingOff by remember { mutableStateOf<String?>(null) }
    var composeDraft by remember { mutableStateOf<MailDraft?>(null) }
    val mailTransport by repository.mailTransport.collectAsStateWithLifecycle()

    val haptics = rememberHaptics()
    fun refresh(collectNow: Boolean = true) {
        if (refreshing) return
        refreshing = true
        scope.launch {
            try { if (collectNow) repository.refreshNow() else repository.refresh(); error = null }
            catch (e: CancellationException) { throw e }
            // Background refreshes stay quiet; a pull or tap reports its result.
            catch (e: Exception) { if (feedback?.failure(e, operation = collectNow) != true) error = res.getString(R.string.inbox_refresh_failed) }
            finally { refreshing = false }
        }
    }
    fun markDone(id: String, done: Boolean) {
        if (id in pending) return
        pending[id] = done
        // As iOS InboxStore: filing buzzes success at once, putting back a light tap.
        haptics(doneHaptic(done))
        scope.launch {
            try { repository.setDone(id, done); error = null }
            catch (e: CancellationException) { throw e }
            catch (e: Exception) { if (feedback?.failure(e) != true) error = res.getString(R.string.inbox_failed_restored) }
            finally { pending.remove(id) }
        }
    }
    // Clean up all / Mark all as seen / Clear all, and their Undo: one request for them all (the hub skips the ones that took new
    // messages since this list), shown at once, then a notice with Undo when something was filed.
    fun markAll(targets: List<persona.core.contracts.api.InboxDoneTarget>, action: InboxBulkAction, undoable: Boolean = true) {
        val done = action != InboxBulkAction.Reopen
        val asked = targets.filter { it.id !in pending }
        if (asked.isEmpty()) return
        asked.forEach { pending[it.id] = done }
        haptics(doneHaptic(done))
        scope.launch {
            try {
                val result = repository.bulk(asked, action)
                // The hub could not write some of them: they are back as they were.
                error = if (result.failed.isEmpty()) null else res.getString(R.string.inbox_failed_restored)
                val notice = if (undoable) bulkNotice(res, result, cleaned = action == InboxBulkAction.Dismiss) else null
                if (notice != null) {
                    val back = result.changed.map { persona.core.contracts.api.InboxDoneTarget(it) }
                    val undo = if (back.isEmpty()) null else FeedbackAction(res.getString(R.string.inbox_undo)) { markAll(back, InboxBulkAction.Reopen, undoable = false) }
                    if (undo != null) feedback?.notice(notice, undo) else feedback?.notice(notice)
                }
            }
            catch (e: CancellationException) { throw e }
            catch (e: Exception) { if (feedback?.failure(e) != true) error = res.getString(R.string.inbox_failed_restored) }
            finally { asked.forEach { pending.remove(it.id) } }
        }
    }
    // "Mute this repository / chat / sender" from a row's menu (iOS `InboxStore.mute`): what it covers is filed at once.
    fun mute(record: InboxRecord, option: InboxMuteOption) {
        scope.launch {
            try {
                repository.mute(record, option)
                error = null
                feedback?.notice(res.getString(R.string.inbox_muted_notice, option.name))
            } catch (e: CancellationException) { throw e }
            catch (e: Exception) { if (feedback?.failure(e) != true) error = res.getString(R.string.inbox_failed_restored) }
        }
    }
    var showMutes by remember { mutableStateOf(false) }
    val sectionReader = rememberInboxSectionReader(conversations, conversationRows)
    if (showMutes) InboxMutesSheet(repository, onDismiss = { showMutes = false })
    LaunchedEffect(repository) { refresh(collectNow = false) }
    // The title while the hub is out of reach opens why, since when and "Retry now" in place (iOS ConnectionTitle).
    val connectionDetails = rememberConnectionDetails(shownConnection, { navigator.navigate(ConnectionSettingsRoute) }, onRetry = { refresh(collectNow = false) })
    fun allTargets(shown: List<InboxGroup>) = shown.filter { it.id != "done" }.flatMap { it.rows }
        .filter { it.type == InboxRowType.Item }.flatMap { row -> conversationRows[row.id]?.records ?: listOfNotNull(records[row.id]) }
        .filterNot { it.item.isDone }.distinctBy { it.item.id }.map { persona.core.contracts.api.InboxDoneTarget(it.item.id, it.item.version) }
    val bulk = allTargets(groups)
    val scopeText = bulkScope(res, section, sources.firstOrNull { it.first == source && source.isNotEmpty() }?.second,
        if (kind == "agent") "Agent" else kindLabel(res, kind), query)
    var showMore by remember { mutableStateOf(false) }
    // Clean up all files every open item of the current view (filters and search) in one request, recorded as dismissed
    // (done_by=dismissed); over BULK_CONFIRM_AT items or more it asks first, for the items shown when it was asked.
    var confirmBulk by remember { mutableStateOf<List<persona.core.contracts.api.InboxDoneTarget>?>(null) }
    fun cleanAll() {
        if (bulk.size >= BULK_CONFIRM_AT) confirmBulk = bulk else markAll(bulk, InboxBulkAction.Dismiss)
    }
    if (showMore) {
        JActionSheet(title = stringResource(R.string.inbox_title), onDismiss = { showMore = false },
            actions = listOf(
                JAction(bulkLabel(res, bulk.size, scopeText), PhIcons.Checks, enabled = bulk.isNotEmpty()) { cleanAll() },
                JAction(res.getString(R.string.inbox_mutes_title), PhIcons.Prohibit, separatorBefore = true) { showMutes = true },
            ),
            note = if (scopeText == null) stringResource(R.string.inbox_bulk_note_all) else stringResource(R.string.inbox_bulk_note_scope, scopeText))
    }
    confirmBulk?.let { targets ->
        JDialog(onDismiss = { confirmBulk = null },
            title = stringResource(R.string.inbox_bulk_confirm_clean_title),
            text = bulkConfirmText(res, targets.size, scopeText),
            confirm = res.getQuantityString(R.plurals.inbox_bulk_confirm_clean, targets.size, targets.size) to {
                confirmBulk = null
                markAll(targets, InboxBulkAction.Dismiss)
            })
    }
    if (showFilters) {
        JActionSheet(title = stringResource(R.string.inbox_filter_title), onDismiss = { showFilters = false }, actions = listOf(
            JAction(stringResource(R.string.inbox_filter_source, sources.firstOrNull { it.first == source }?.second ?: stringResource(R.string.inbox_all_sources)), PhIcons.Tray) { filterPage = "source" },
            JAction(stringResource(R.string.inbox_filter_kind, kindLabel(res, kind).ifBlank { if (kind == "agent") "Agent" else res.getString(R.string.inbox_all_kinds) }), PhIcons.FunnelSimple) { filterPage = "kind" },
            JAction(stringResource(R.string.inbox_filter_clear), enabled = source.isNotEmpty() || kind.isNotEmpty()) {
                filter(currentFilters.copy(source = "", kind = ""))
            },
        ))
    }
    if (filterPage == "source") JSheet(onDismiss = { filterPage = null }, title = stringResource(R.string.inbox_source)) {
        // The source list can grow with connected accounts, so keep the choices scrollable.
        androidx.compose.foundation.lazy.LazyColumn {
            itemsIndexed(sources) { index, option ->
                JGroupItem(GroupPosition.of(index, sources.size)) {
                    JRow(option.second, onClick = { filter(currentFilters.copy(source = option.first)); filterPage = null },
                        trailing = { JRadio(source == option.first, null) })
                }
            }
        }
    }
    if (filterPage == "kind") JActionSheet(title = stringResource(R.string.inbox_kind), onDismiss = { filterPage = null }, actions =
        listOf("" to res.getString(R.string.inbox_all_kinds), "mention" to res.getString(R.string.inbox_mention_me), "dm" to res.getString(R.string.inbox_kind_dm),
            "group" to res.getString(R.string.inbox_kind_group), "mail" to res.getString(R.string.inbox_source_mail), "agent" to "Agent").map { (value, label) ->
            JAction(label, checked = kind == value) { filter(currentFilters.copy(kind = value)) }
        })
    // Loading shows only while there is nothing to show yet: a row put in over a shown list pushes it down, then goes.
    val loadingFirst = snapshot.loading && snapshot.records.isEmpty() && snapshot.interactions.isEmpty()
    val failedEmpty = snapshot.records.isEmpty() && !loadingFirst && (error ?: snapshot.error) != null
    // One remembered list per filter. The query and the folds stay out of the key: a new key recreates the page, so
    // typing would drop the search field's focus (and the keystroke) and folding a section would jump to the top.
    listStates.SaveableStateProvider(listOf(source, kind, section).toString()) {
        val scroll = rememberLazyListState()
        JListPage(
            title = stringResource(R.string.inbox_title),
            summary = inboxSummary(res, overall, ::working),
            subtitle = inboxTitle(shownConnection).takeIf { shownConnection != ConnectionState.Online },
            titleBusy = shownConnection in setOf(ConnectionState.Connecting, ConnectionState.Updating),
            onTitleClick = if (shownConnection == ConnectionState.Online) null else connectionDetails,
            state = scroll, refreshing = refreshing, onRefresh = { refresh() },
            actions = {
                if (mailTransport != null) {
                    JIconButton(PhIcons.PencilSimple, stringResource(R.string.inbox_compose_mail), {
                        composeDraft = MailDraft(accountId = "")
                    })
                }
                JIconButton(PhIcons.FunnelSimple, stringResource(R.string.inbox_filter), { showFilters = true }, badge = source.isNotEmpty() || kind.isNotEmpty())
                JIconButton(PhIcons.DotsThree, stringResource(R.string.inbox_more), { showMore = true })
            },
        ) {
            item("filters") {
                QuickFilterBar(listOf(Bay(stringResource(R.string.inbox_bay_needs_you), needsCount, Status.NeedsYou), Bay(stringResource(R.string.inbox_bay_all), allCount),
                    Bay(stringResource(R.string.inbox_done), doneCount)),
                    selected = when (section) { "action" -> 0; "done" -> 2; else -> 1 },
                    onSelect = { filter(currentFilters.copy(section = listOf("action", "", "done")[it])) })
                Spacer(Modifier.height(Tokens.Space.s))
                JSearchField(query, { filter(currentFilters.copy(query = it)) }, stringResource(R.string.inbox_search_hint))
            }
            // New items waiting outside the list for triage (#1773): one line over the first item until triage is ok.
            InboxTriageText.banner(res, snapshot.triage)?.let { banner -> item("triage") { InboxTriageNotice(banner) } }
            // Sources the hub could not read this round (#1849): what they show is the last round's.
            InboxTriageText.unread(res, snapshot.triage)?.let { text -> item("triage-unread") { InboxUnreadNotice(text) } }
            if (loadingFirst) item("loading") { JLoading(text = stringResource(R.string.inbox_loading)) }
            if (!failedEmpty) (error ?: snapshot.error?.takeIf { feedback == null || connection == ConnectionState.Online })?.let { message ->
                item("error") { StatusText(message, isError = true) }
            }
            if (preferenceError) item("preference-error") { StatusText(stringResource(R.string.inbox_filters_not_saved), isError = true) }
            sectionReader.notice?.let { notice -> item("section-read-result") {
                JNotice(notice, actionLabel = sectionReader.retry?.let { stringResource(R.string.inbox_retry) },
                    onAction = sectionReader.retry)
            } }
            if (groups.isEmpty() && !loadingFirst) item("empty") {
                val filtered = query.isNotEmpty() || source.isNotEmpty() || kind.isNotEmpty() || section.isNotEmpty()
                when {
                    !filtered && failedEmpty -> JEmptyState(stringResource(R.string.inbox_empty_failed_title), stringResource(R.string.inbox_empty_failed_text), action = {
                        JButton(stringResource(R.string.inbox_retry), { refresh() }, enabled = !refreshing, kind = JButtonKind.Secondary)
                    })
                    query.isNotBlank() -> JEmptyState(stringResource(R.string.inbox_empty_search_title), stringResource(R.string.inbox_empty_search_text, query.trim()), icon = PhIcons.MagnifyingGlass)
                    snapshot.records.isEmpty() && snapshot.interactions.isEmpty() -> JEmptyState(stringResource(R.string.inbox_empty_title),
                        stringResource(R.string.inbox_empty_text), icon = PhIcons.Tray)
                    else -> JEmptyState(stringResource(R.string.inbox_empty_filtered_title), stringResource(R.string.inbox_empty_filtered_text), icon = PhIcons.CheckCircle)
                }
            }
            // Several managers (docs/assistant/managers.md step 4): each one's inbox under its name, its sections inside.
            val byManager = inboxByManager(roster, groups) { row ->
                if (row.type == InboxRowType.Interaction) interactions[row.id]?.manager else records[row.id]?.manager
            }
            val sections: (String, List<InboxGroup>) -> Unit = { prefix, shownGroups -> shownGroups.forEach { group ->
                item("section:$prefix${group.id}", contentType = "section") {
                    JSectionHeader(
                        title = if (group.id == "working") group.title.text() else inboxGroupTitle(group),
                        count = group.count,
                        status = when (group.id) { "action" -> Status.NeedsYou; "working" -> Status.Working; else -> null },
                        trailing = if (group.id == "working") null else if (group.folded) stringResource(R.string.inbox_expand) else stringResource(R.string.inbox_collapse),
                        onClick = if (group.id == "working") null else ({
                            filter(currentFilters.copy(section = if (section == group.id) "" else section,
                                expanded = if (group.id in expanded) expanded - group.id else expanded + group.id))
                        }),
                        action = if (group.id in setOf("action", "fyi", "noise")) ({
                            JTextButton(if (group.id != "noise") stringResource(R.string.inbox_conversations_read_all) else stringResource(R.string.inbox_clear_all), {
                                if (group.id != "noise") sectionReader.read(group) else markAll(allTargets(listOf(group)), InboxBulkAction.Done)
                            }, enabled = !sectionReader.busy)
                        }) else null,
                    )
                }
                itemsIndexed(group.visibleRows, key = { _, row -> "$prefix${row.type}:${row.id}" }, contentType = { _, row -> row.type }) { index, row ->
                    val position = GroupPosition.of(index, group.visibleRows.size)
                    if (row.type == InboxRowType.Interaction) {
                        interactions[row.id]?.let { interaction ->
                            MessageItem(sender = "Persona", title = interaction.sessionTitle.ifBlank { interaction.title },
                                preview = interaction.summary ?: interaction.title, position = position, status = Status.NeedsYou,
                                onClick = {
                                    // A card outside any session (`personad ask` in a plain terminal) has no chat to open.
                                    if (interaction.sessionId.isNotBlank()) navigator.navigate(ChatRoute(interaction.machineId, interaction.sessionId))
                                    else onOpenCard?.invoke(persona.core.navigation.RequestRef(interaction.machineId, interaction.id))
                                        ?: navigator.selectTab(persona.core.navigation.AppTab.Agents)
                                })
                        }
                    } else conversationRows[row.id]?.let { conversation ->
                        val done = conversation.records.all { pending[it.item.id] ?: it.item.isDone }
                        InboxConversationItemRow(conversation, navigator, position, onlineSince,
                            done = done, busy = conversation.records.any { it.item.id in pending },
                            onHandOff = { handingOff = conversation.itemId },
                            onDone = { markAll(conversation.records.filter { it.item.isDone == done }.map { persona.core.contracts.api.InboxDoneTarget(it.item.id, it.item.version) },
                                if (done) InboxBulkAction.Reopen else InboxBulkAction.Done) })
                    } ?: records[row.id]?.let { record ->
                        val done = pending[record.item.id] ?: record.item.isDone
                        InboxItemRow(record, repository, navigator, composers, position, done, record.item.id in pending, onlineSince,
                            onDone = { markDone(record.item.id, !done) }, onHandOff = { handingOff = record.item.id },
                            onMute = { option -> mute(record, option) })
                    }
                }
            } }
            if (byManager == null) sections("", groups)
            else byManager.forEach { (manager, shownGroups) ->
                item("manager:${manager.id}", contentType = "manager") { JManagerHeader(manager, count = manager.items.size) }
                sections("${manager.id}:", shownGroups)
            }
        }
    }
    handingOff?.let { id -> records[id] }?.let { record ->
        InboxHandOffSheet(repository, record, navigator, onDismiss = { handingOff = null })
    }
    composeDraft?.let { draft ->
        mailTransport?.let { transport ->
            ComposeMailDialog(
                transport = transport,
                draft = draft,
                onDone = { composeDraft = null },
            )
        }
    }
}

/**
 * The inbox's sections split by the manager each row belongs to ([managerOf]; no stamp is the default's), the default
 * manager first, then the roster's order; a manager with no rows is left out. Null with one manager.
 */
internal fun inboxByManager(
    roster: persona.core.contracts.api.ManagerRoster?, groups: List<InboxGroup>, managerOf: (InboxRow) -> String?,
): List<Pair<persona.core.contracts.api.ManagerGroup<InboxRow>, List<InboxGroup>>>? {
    val byManager = roster?.groups(groups.flatMap { it.rows }.distinct(), managerOf) ?: return null
    return byManager.map { manager ->
        val mine = manager.items.toSet()
        manager to groups.mapNotNull { group ->
            group.rows.filter { it in mine }.takeIf { it.isNotEmpty() }?.let { rows -> group.copy(rows = rows, visibleRows = group.visibleRows.filter { it in mine }) }
        }
    }
}

/** A section's title by its id; :core:domain's titles are Chinese, and a group this app does not know keeps its own. */
@Composable
private fun inboxGroupTitle(group: InboxGroup): String = when (group.id) {
    "action" -> stringResource(R.string.inbox_bay_needs_you)
    "working" -> stringResource(R.string.inbox_group_working)
    "fyi" -> stringResource(R.string.inbox_group_fyi)
    "noise" -> stringResource(R.string.inbox_section_folded)
    "done" -> stringResource(R.string.inbox_done)
    "read-pending" -> stringResource(R.string.inbox_conversations_read_pending)
    else -> group.title.text()
}

/** A tier's name, as the item's header tags it. */
@Composable
internal fun inboxTierTitle(tier: InboxTier): String = stringResource(when (tier) {
    InboxTier.Action -> R.string.inbox_bay_needs_you
    InboxTier.Fyi -> R.string.inbox_tier_fyi
    InboxTier.Noise -> R.string.inbox_section_folded
})

@Composable
internal fun StatusText(text: String, isError: Boolean = false) {
    JNotice(text, Modifier.padding(vertical = Tokens.Space.s), status = if (isError) Status.NeedsYou else Status.Unknown)
}

/**
 * The page's one line, for the whole inbox: give it the unfiltered list, since a search or a filter that shows nothing
 * is not "All caught up". Rows an agent is working on are Persona's, not the user's to answer.
 */
internal fun inboxSummary(res: Resources, inbox: InboxListResult, working: (InboxRow) -> Boolean): String {
    val open = inbox.groups.filter { it.id != "done" }
    val busy = open.sumOf { group -> group.rows.count(working) }
    val needs = needsYouCount(inbox, working)
    return if (needs == 0) res.getString(R.string.inbox_summary_clear) else res.getString(R.string.inbox_summary, needs, busy)
}

private fun kindLabel(res: Resources, kind: String) = when (kind) {
    "mention" -> res.getString(R.string.inbox_mention_me); "dm" -> res.getString(R.string.inbox_kind_dm)
    "group" -> res.getString(R.string.inbox_kind_group); "bot" -> res.getString(R.string.inbox_bot)
    "mail" -> res.getString(R.string.inbox_source_mail); "moment" -> res.getString(R.string.inbox_moments); else -> ""
}

/** The row's kind: an @ of everyone is "@all", a mail the user was only copied on is "Cc" (the old app's labels). */
internal fun kindLabel(res: Resources, record: InboxRecord): String = when {
    // A connector names its own kinds ("Direct message", "@me", "Transfer", "Notice"…): its label, never one this app guesses from the kind.
    record.isConnector -> record.connector?.label.orEmpty()
    record.item.kind == "mention" && record.read -> res.getString(R.string.inbox_kind_group)
    record.item.kind == "mention" && record.atAll -> res.getString(R.string.inbox_kind_at_all)
    record.item.kind == "mail" && record.mail?.direct == false -> res.getString(R.string.inbox_kind_cc)
    record.doc != null || record.item.kind == "doc" -> docKindLabel(res, record.doc)
    record.moment != null || record.item.kind == "moment" ->
        res.getString(if (record.moment?.notice?.startsWith("atIn") == true) R.string.inbox_mention_me else R.string.inbox_moments)
    else -> kindLabel(res, record.item.kind)
}

/**
 * The connection state the inbox title shows. Every reconnect passes through Connecting and Updating, usually for a
 * moment; shown at once, they put a spinner and a line under the title and push the list down, then take them away.
 * Coming from Online they show once they have lasted [delayMillis]; every other change shows at once.
 */
@Composable
internal fun rememberSettledConnection(connection: ConnectionState, delayMillis: Long = 1_000): ConnectionState {
    val brief = connection == ConnectionState.Connecting || connection == ConnectionState.Updating
    var shown by remember { mutableStateOf(if (brief) ConnectionState.Online else connection) }
    LaunchedEffect(connection) {
        if (brief && shown == ConnectionState.Online) kotlinx.coroutines.delay(delayMillis)
        shown = connection
    }
    return shown
}

@Composable
internal fun inboxTitle(state: ConnectionState): String = stringResource(when (state) {
    ConnectionState.Online -> R.string.inbox_title
    ConnectionState.Connecting -> R.string.inbox_conn_connecting
    ConnectionState.Updating -> R.string.inbox_conn_updating
    ConnectionState.WaitingForNetwork -> R.string.inbox_conn_waiting_network
    ConnectionState.NeedsLocalNetworkPermission -> R.string.inbox_conn_local_network
    ConnectionState.CertificateMismatch -> R.string.inbox_conn_certificate
    ConnectionState.Revoked -> R.string.inbox_conn_revoked
})

/** [InboxRecord.localStatus] tokens the repositories write (`RealInboxRepository`, [InboxExperienceRepository]). */
internal const val LOCAL_DRAFT = "草稿 · " // i18n-ignore: token the repositories write, shown via inbox_local_draft
internal const val LOCAL_UNCONFIRMED = "结果待确认 · 打开核对" // i18n-ignore: token, shown via inbox_local_unconfirmed
