package persona.feature.settings

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import android.content.res.Resources
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import persona.core.contracts.api.ConnectorsManagement
import persona.core.contracts.api.HubConnector
import persona.core.contracts.Machine
import persona.core.contracts.api.HubExtension
import persona.core.contracts.api.ExtensionDetail
import persona.core.contracts.api.ExtensionField
import persona.core.contracts.api.ExtensionManagement
import persona.core.contracts.api.ExtensionSecret
import persona.core.contracts.api.ExtensionUiPlace
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.navigation.LocalDetailKey
import persona.core.navigation.ExtensionMarketRoute
import persona.core.navigation.ExtensionRoute
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.contentOrNull

/**
 * Settings › Extensions (docs/extensions/extensions.md "App: Settings › Extensions"): every extension of the hub's (built in, from hub.json, installed on
 * the hub's machine), its state, and the way to its page. Extensions are installed on the hub's machine only.
 */
@Composable
internal fun ExtensionsPage(extensions: ExtensionManagement?, onBack: () -> Unit, onExtension: (String, String?) -> Unit, onMarket: (() -> Unit)? = null) {
    if (extensions == null) {
        JListPage(title = stringResource(R.string.settings_ext_title), onBack = onBack) { item { JNotice(stringResource(R.string.settings_ext_demo_none)) } }
        return
    }
    val res = LocalResources.current
    val model = remember(extensions, res) { ExtensionListModel(extensions, res) }
    val state by model.state.collectAsState()
    val scope = rememberCoroutineScope()
    LaunchedEffect(model) { model.load() }
    // The page beside it (a wide window) removed or changed one: the list follows without a pull.
    LaunchedEffect(model) { ExtensionChanges.of(extensions).collect { model.load() } }
    // Signed in or out somewhere else: " · Not signed in" follows.
    LaunchedEffect(model) { model.watchAuth() }
    val list = state.extensions
    val roster = LocalManagerRoster.current
    val detail = LocalDetailKey.current
    JListPage(title = stringResource(R.string.settings_ext_title), onBack = onBack, refreshing = state.loading && list != null, onRefresh = { scope.launch { model.load() } }) {
        state.error?.let { error ->
            item("error") {
                JNotice(error, Modifier.padding(top = Tokens.Space.m), status = Status.NeedsYou,
                    actionLabel = stringResource(R.string.settings_ext_retry), onAction = { scope.launch { model.load() } })
            }
        }
        // "Extension market" only when the hub has markets (docs/extensions/extensions.md "Extension market"): the one way a phone installs an extension.
        if (state.markets && onMarket != null) item("market") {
            JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.m)) {
                JRow(stringResource(R.string.settings_ext_market), subtitle = stringResource(R.string.settings_ext_market_subtitle), leadingIcon = PhIcons.Cube,
                    selected = detail == ExtensionMarketRoute, onClick = onMarket)
            }
        }
        if (list == null && state.loading) item("loading") { JLoading() }
        if (list != null && list.isEmpty()) item("empty") {
            JEmptyState(stringResource(R.string.settings_ext_empty_title), stringResource(R.string.settings_ext_empty_text), icon = PhIcons.PuzzlePiece)
        }
        if (!list.isNullOrEmpty()) {
            // Several managers (docs/assistant/managers.md step 4): each one's extensions under its name instead of one list.
            val byManager = roster?.groups(list) { it.manager }
            val rows: LazyListScope.(List<HubExtension>) -> Unit = { shown ->
                // keyed by manager and id in every path: a built-in is listed under each manager, and the list can come
                // before the roster; a pair that still repeats (a hub that stamps nothing) takes its place too
                val repeated = shown.groupingBy { it.manager.orEmpty() to it.id }.eachCount().filterValues { it > 1 }.keys
                itemsIndexed(shown, key = { index, it ->
                    "plugin-${it.manager.orEmpty()}-${it.id}" + if ((it.manager.orEmpty() to it.id) in repeated) "-$index" else ""
                }) { index, extension ->
                    JGroupItem(GroupPosition.of(index, shown.size)) {
                        JRow(extension.name.ifBlank { extension.id }, subtitle = ExtensionText.listLine(res, extension), leadingIcon = PhIcons.PuzzlePiece,
                            trailing = { ExtensionStateMark(extension) }, chevron = true,
                            selected = (detail as? ExtensionRoute)?.let { it.extensionId == extension.id && roster.ownerOrDefault(it.manager) == roster.ownerOrDefault(extension.manager) } == true,
                            onClick = { onExtension(extension.id, extension.manager?.takeIf { it != roster?.defaultManagerId }) })
                    }
                }
            }
            if (byManager == null) {
                item("top") { JSectionHeader(stringResource(R.string.settings_ext_list_header), count = list.size) }
                rows(list)
            } else byManager.forEach { group ->
                item("manager-${group.id}") { JManagerHeader(group, count = group.items.size) }
                rows(group.items)
            }
            item("footer") {
                SettingsCaption(stringResource(if (state.markets && onMarket != null) R.string.settings_ext_list_footer_market
                    else R.string.settings_ext_list_footer),
                    Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.m))
            }
        }
    }
}

/** The mark beside an extension's state: the style's status dot, or a warning in the danger colour when it is broken. */
@Composable
private fun ExtensionStateMark(extension: HubExtension?) {
    when {
        extension == null -> JStatusDot(Status.Unknown)
        ExtensionText.isProblem(extension) -> JIcon(PhIcons.WarningCircle, ExtensionText.state(LocalResources.current, extension), tint = PersonaTheme.palette.danger.dot)
        else -> JStatusDot(ExtensionText.status(extension))
    }
}

/**
 * Settings › Extensions › one: what it is and how it is doing, its switch, its settings as a form drawn from its schema, its
 * secrets (set here, never shown again), what it contributes and what it may do. An extension written in hub.json is only
 * looked at here.
 */
@Composable
fun ExtensionPage(
    extensions: ExtensionManagement?, extensionId: String, onBack: () -> Unit, onPage: ((ExtensionUiPlace) -> Unit)? = null,
    /** Opens one of the channels it contributes (Settings › Channels); without it the line is left out. */
    onChannel: ((String) -> Unit)? = null,
    /** The connectors it contributes, for their "Send to Inbox" switch (the hub's own, as on Settings › Connectors); null leaves it out. */
    connectors: ConnectorsManagement? = null,
    /** Opens one of those connectors' page; without it the link is left out. */
    onConnector: ((String) -> Unit)? = null,
    /** The hub's machines, for a field that lists machines (format machine); without them such a list shows only its values. */
    machines: Flow<List<Machine>>? = null,
    /** Opens a newer version in the market (market id, extension id) to review and upgrade it; without it the line only says. */
    onUpgrade: ((String, String) -> Unit)? = null,
    /** Just installed and not signed in (`next: auth`, #4507): its sign-in starts once the page knows it. */
    startSignIn: Boolean = false,
    /** Whose extension it is (several managers); null: the default's. Its requests name it while the page is open. */
    manager: String? = null,
) {
    PinnedExtension(extensionId, manager)
    if (extensions == null) {
        JListPage(title = stringResource(R.string.settings_ext_title), onBack = onBack) { item { JNotice(stringResource(R.string.settings_ext_demo_none)) } }
        return
    }
    val res = LocalResources.current
    val model = remember(extensions, extensionId, res, manager) { ExtensionModel(extensions, extensionId, res, manager) }
    val state by model.state.collectAsState()
    val scope = rememberCoroutineScope()
    val feedback = LocalAppFeedback.current
    var editingSecret by remember { mutableStateOf<ExtensionSecret?>(null) }
    var choosing by remember { mutableStateOf<Pair<ExtensionField, List<String>>?>(null) }
    var removing by remember { mutableStateOf(false) }
    // Only what a machine's row shows: live metrics would redraw the page every few seconds.
    val machineList by remember(machines) {
        (machines ?: flowOf(emptyList())).map { list -> list.map { ExtensionMachine(it.id, it.displayName, it.os, it.online, it.computer) } }.distinctUntilChanged()
    }.collectAsState(emptyList())
    LaunchedEffect(model) { model.load() }
    // Its sign-in (#4507): the hub's pushes, and the steps a sheet shows.
    LaunchedEffect(model) { model.watchAuth() }
    val signIn = remember(extensions, extensionId) {
        ConnectorSignInModel({ step, values -> extensions.authStep(extensionId, step, values) }, { model.setAuth(extensions.authLogout(extensionId)) })
    }
    val signInState by signIn.state.collectAsState()
    var signOutError by remember { mutableStateOf<String?>(null) }
    var confirmingSignOut by remember { mutableStateOf(false) }
    val polled = signInState.bind.prompt?.takeIf { signInState.bind.polled }
    LaunchedEffect(signIn, polled?.step, polled?.kind, polled?.userCode, polled?.url) { if (polled != null) signIn.poll() }
    LaunchedEffect(signInState.signedInAs) {
        val who = signInState.signedInAs ?: return@LaunchedEffect
        feedback?.notice(if (who.isBlank()) res.getString(R.string.settings_ext_auth_signed_in) else res.getString(R.string.settings_ext_auth_signed_in_as, who))
        signIn.signedInShown()
        model.load()
        ExtensionChanges.changed(extensions)
    }
    val detail = state.detail
    val auth = detail?.extension?.auth
    var autoStarted by androidx.compose.runtime.saveable.rememberSaveable(extensionId) { mutableStateOf(false) }
    LaunchedEffect(startSignIn, auth != null) {
        if (!startSignIn || autoStarted || auth == null) return@LaunchedEffect
        autoStarted = true
        if (!auth.signedIn && detail.extension.enabled != false) signIn.start()
    }
    val contributed = detail?.takeIf { it.extension.enabled ?: true }?.extension?.contributes.orEmpty()["connectors"].orEmpty()
    val roster = LocalManagerRoster.current
    PinnedExtension(contributed.map { listOf("connectors", it) }, manager)
    var hubConnectors by remember(connectors, extensionId, manager) { mutableStateOf<List<HubConnector>>(emptyList()) }
    LaunchedEffect(connectors, contributed) {
        if (connectors == null || contributed.isEmpty()) return@LaunchedEffect
        // The list is every manager's (docs/assistant/managers.md): this page's are its manager's, whose requests it pins below.
        hubConnectors = try {
            connectors.connectors(all = true)
                .filter { it.id in contributed && roster.ownerOrDefault(it.manager) == roster.ownerOrDefault(manager) }.distinctBy { it.id }
        }
        catch (e: CancellationException) { throw e } catch (_: Exception) { emptyList() } // the connector's page says what is wrong
    }
    val hasForm = detail != null && !detail.readOnly && detail.form.isNotEmpty()
    // A form of machine lists only saves each switch at once: nothing waits for "Save".
    val waitsForSave = hasForm && detail.form.flatMap { if (it.type == "object") it.fields.orEmpty() else listOf(it) }.any { it.items?.machines != true }
    JListPage(title = detail?.extension?.name?.ifBlank { null } ?: stringResource(R.string.settings_ext_title), onBack = onBack, largeTitle = false,
        refreshing = state.loading && detail != null, onRefresh = { scope.launch { model.load() } },
        bottomBar = if (!waitsForSave) null else ({
            Row(Modifier.fillMaxWidth().padding(horizontal = pagePadding, vertical = Tokens.Space.s)) {
                JButton(stringResource(R.string.settings_ext_save), { scope.launch { if (model.save()) feedback?.notice(res.getString(R.string.settings_ext_saved_restart)) } }, Modifier.fillMaxWidth(),
                    loading = state.saving, enabled = state.canSave)
            }
        })) {
        header(extensionId, state, res, onRetry = { scope.launch { model.load() } })
        if (auth != null) extensionSignIn(auth, enabled = detail.extension.enabled != false, starting = signInState.bind.sending && signInState.bind.prompt == null,
            error = signInState.bind.error?.resolve(res)?.takeIf { signInState.bind.prompt == null } ?: signOutError, busy = state.busy,
            onSignIn = { signOutError = null; scope.launch { signIn.start() } }, onSignOut = { confirmingSignOut = true })
        if (detail != null && onChannel != null) channelLink(detail, onChannel)
        if (detail != null) {
            if (detail.readOnly) item("read-only") {
                JNotice(stringResource(R.string.settings_ext_read_only), Modifier.padding(top = Tokens.Space.m))
            } else {
                item("enabled") {
                    JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.m)) {
                        JSwitchRow(stringResource(R.string.settings_ext_enabled), detail.extension.enabled ?: true, { on -> scope.launch { model.setEnabled(on) } }, enabled = !state.busy)
                    }
                }
                item("enabled-note") { Note(stringResource(R.string.settings_ext_enabled_note)) }
            }
            if (detail.extension.autoUpgrade != null) upgrades(detail.extension, state.busy, res, onUpgrade,
                onToggle = { on ->
                    scope.launch {
                        if (model.setAutoUpgrade(on)) feedback?.notice(res.getString(if (on) R.string.settings_ext_auto_upgrade_on else R.string.settings_ext_auto_upgrade_off))
                    }
                })
            // The extension's own sign-in page is for older apps: this one signs in above (#4507).
            if (onPage != null) extensionPages(detail.ui.filter { auth?.page.isNullOrBlank() || it.id != auth.page }, onPage)
            if (connectors != null) connectorInbox(hubConnectors, onConnector) { c, on ->
                scope.launch {
                    try {
                        val changed = connectors.setConnectorFeed(c.id, "inbox", on)
                        hubConnectors = hubConnectors.map { if (it.id == changed.id) changed else it }
                    } catch (e: CancellationException) { throw e } catch (e: Exception) {
                        feedback?.notice(res.getString(R.string.settings_src_save_failed, e.message.orEmpty()))
                    }
                }
            }
            if (hasForm) form(detail, state, model, machineList, onChoose = { field, path -> choosing = field to path })
            if (!detail.readOnly && detail.secrets.isNotEmpty()) secrets(detail, state, onEdit = { editingSecret = it })
            contributions(detail, state.busy || state.saving, res) { item, on -> scope.launch { model.setContribution(item, on) } }
            sandbox(detail.extension.sandbox)
            permissions(detail)
            about(detail, res)
            // From a market: removed here too, as in the market (docs/extensions/extensions.md "Extension market"), read-only config or not.
            if (detail.fromMarket) uninstallRow(state.busy) { removing = true }
        }
    }
    if (auth != null) ExtensionSignInSheet(auth, detail.extension.name.ifBlank { extensionId }, signInState.bind, signIn)
    if (confirmingSignOut && auth != null) {
        JDialog({ confirmingSignOut = false }, title = res.getString(R.string.settings_ext_auth_sign_out_title, ExtensionAuthText.title(res, auth)),
            text = stringResource(R.string.settings_ext_auth_sign_out_text), destructive = true,
            confirm = stringResource(R.string.settings_ext_auth_sign_out) to {
                confirmingSignOut = false
                scope.launch {
                    signOutError = signIn.logout()?.resolve(res)
                    if (signOutError == null) {
                        feedback?.notice(res.getString(R.string.settings_ext_auth_signed_out_notice))
                        ExtensionChanges.changed(extensions)
                    }
                }
            })
    }
    val current = detail
    if (removing && current != null) UninstallDialog(current.extension.name.ifBlank { current.extension.id }, onDismiss = { removing = false }) { keep ->
        removing = false
        scope.launch {
            if (model.uninstall(keep)) {
                feedback?.notice(res.getString(R.string.settings_ext_uninstalled, current.extension.name.ifBlank { current.extension.id }))
                onBack()
            }
        }
    }
    // Others depend on it (#5192): named, and switched off with it once agreed.
    state.cascade?.let { ask ->
        val name = detail?.extension?.name?.ifBlank { null } ?: extensionId
        JDialog({ model.dismissCascade() }, title = stringResource(R.string.settings_ext_dependents_title), text = ExtensionText.cascade(res, ask, name),
            destructive = true, confirm = stringResource(if (ask.keepData == null) R.string.settings_ext_dependents_off else R.string.settings_ext_dependents_remove) to {
                scope.launch {
                    if (model.confirmCascade() && ask.keepData != null) {
                        feedback?.notice(res.getString(R.string.settings_ext_uninstalled, name))
                        onBack()
                    }
                }
            })
    }
    choosing?.let { (field, path) ->
        val current = state.value(path)
        JActionSheet(listOf(JAction(ExtensionText.placeholder(res, field), checked = current == null, onClick = { model.edit(path, null) })) +
            field.enum.orEmpty().map { choice ->
                JAction(choice.label, checked = current == choice.value, onClick = { model.edit(path, choice.value) })
            }, { choosing = null }, title = ExtensionText.fieldTitle(res, field))
    }
    editingSecret?.let { secret ->
        ExtensionSecretSheet(secret, busy = state.busy, onDismiss = { editingSecret = null },
            save = { value -> model.setSecret(secret.name, value) }, clear = { model.clearSecret(secret.name) })
    }
}

@Composable
private fun Note(text: String) = SettingsCaption(text, Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s))

private fun LazyListScope.header(id: String, state: ExtensionState, res: Resources, onRetry: () -> Unit) {
    val extension = state.detail?.extension
    item("header") {
        JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.s)) {
            JRow(extension?.name?.ifBlank { null } ?: id, subtitle = extension?.let { ExtensionText.stateLine(res, it) } ?: stringResource(if (state.loading) R.string.settings_ext_loading else R.string.settings_ext_unavailable),
                leadingIcon = PhIcons.PuzzlePiece, trailing = { if (extension == null && state.loading) JSpinner() else ExtensionStateMark(extension) })
        }
    }
    extension?.description?.takeIf { it.isNotBlank() }?.let { item("description") { Note(it) } }
    extension?.let { ExtensionText.runNote(res, it) }?.let { item("run-note") { Note(it) } }
    extension?.needsSetup?.takeIf { it.isNotEmpty() }?.let { needs ->
        item("needs") { JInlineStatus(stringResource(R.string.settings_ext_still_needed, needs.joinToString(res.getString(R.string.settings_ext_list_separator))), Modifier.padding(top = Tokens.Space.s), tone = InlineTone.Caution) }
    }
    extension?.error?.takeIf { it.isNotBlank() }?.let { error ->
        item("plugin-error") { JInlineStatus(error, Modifier.padding(top = Tokens.Space.s), tone = InlineTone.Failed) }
    }
    state.error?.let { error ->
        item("error") {
            Column(Modifier.padding(top = Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                JInlineStatus(error, tone = InlineTone.Failed)
                if (extension == null) JButton(stringResource(R.string.settings_ext_retry), onRetry, Modifier.fillMaxWidth(), kind = JButtonKind.Secondary, enabled = !state.loading)
            }
        }
    }
}

/**
 * An extension that is a way of talking to Persona (it contributes a `channel`, docs/extensions/channels.md "UI") says where that is
 * set up: binding, its switch and where its messages go are on the channel's page; this page keeps working as before.
 */
private fun LazyListScope.channelLink(detail: ExtensionDetail, onChannel: (String) -> Unit) {
    val id = detail.extension.contributes.orEmpty()["channel"]?.firstOrNull { it.isNotBlank() } ?: return
    item("channel-link") {
        JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.m)) {
            JRow(stringResource(R.string.settings_ext_channel_link), subtitle = stringResource(R.string.settings_ext_channel_link_subtitle), leadingIcon = PhIcons.ChatsCircle, onClick = { onChannel(id) })
        }
    }
}

/**
 * The connectors an extension contributes (persona#3178): each one's "Send to Inbox" switch, the hub's own switch (the same as on
 * Settings › Connectors › ...), and the way to the connector's page for the rest. A connector without an inbox is left out.
 */
private fun LazyListScope.connectorInbox(list: List<HubConnector>, onOpen: ((String) -> Unit)?, onInbox: (HubConnector, Boolean) -> Unit) {
    list.filter { it.inbox != null }.forEach { c ->
        item("connector-${c.id}-header") { JSectionHeader(stringResource(R.string.settings_ext_connector_title, c.name)) }
        item("connector-${c.id}-inbox") {
            JGroupItem(if (onOpen == null) GroupPosition.Single else GroupPosition.of(0, 2)) {
                JSwitchRow(stringResource(R.string.settings_src_feed_inbox), checked = c.inbox ?: false, onCheckedChange = { onInbox(c, it) })
            }
        }
        if (onOpen != null) item("connector-${c.id}-open") {
            JGroupItem(GroupPosition.of(1, 2)) {
                JRow(stringResource(R.string.settings_ext_connector_open), subtitle = stringResource(R.string.settings_ext_connector_open_subtitle),
                    leadingIcon = PhIcons.PlugsConnected, onClick = { onOpen(c.id) })
            }
        }
        item("connector-${c.id}-note") {
            Note(if (c.inbox == true) stringResource(R.string.settings_ext_connector_inbox_on, c.name) else stringResource(R.string.settings_ext_connector_inbox_off))
        }
    }
}

/**
 * A market extension's upgrades (docs/extensions/extensions.md "Auto-upgrade"): its own switch, its newer version (opening the market's
 * upgrade when it waits for the user) or what happened last.
 */
private fun LazyListScope.upgrades(
    extension: HubExtension, busy: Boolean, res: Resources, onUpgrade: ((String, String) -> Unit)?, onToggle: (Boolean) -> Unit,
) {
    val upgrade = extension.upgrade
    val note = if (upgrade == null) extension.upgradeNote?.let { n -> ExtensionText.upgradeNote(res, n)?.let { it to n } } else null
    val count = 1 + (if (upgrade != null) 1 else 0) + (if (note != null) 1 else 0)
    // Paused (switched off, its market removed…), the extension is not upgraded: its switch shows off and waits (the hub keeps it).
    val paused = extension.autoUpgradePaused != null
    item("upgrade-title") { JSectionHeader(stringResource(R.string.settings_ext_upgrade_title)) }
    item("upgrade-auto") {
        JGroupItem(GroupPosition.of(0, count)) {
            JSwitchRow(stringResource(R.string.settings_ext_auto_upgrade), ExtensionText.autoUpgradeShown(extension), onToggle, enabled = !busy && !paused)
        }
    }
    upgrade?.let { u ->
        item("upgrade-new") {
            // Only a known market can open the upgrade; without one the line only says.
            val action = ExtensionText.upgradeAction(res, u)?.takeIf { onUpgrade != null && u.market.isNotBlank() }
            val detail = ExtensionText.upgradeDetail(res, u).takeIf { it.isNotBlank() }
            JGroupItem(GroupPosition.of(1, count)) {
                if (action == null) JRow(stringResource(R.string.settings_ext_new_version, u.to), subtitle = detail, subtitleLines = 4, leadingIcon = PhIcons.ArrowUp)
                else JRow(stringResource(R.string.settings_ext_new_version, u.to), subtitle = detail, subtitleLines = 4, leadingIcon = PhIcons.ArrowUp,
                    // Waiting on the user (more permissions, or a version that rolled back) stands out; the rest is a plain link.
                    value = action.takeUnless { ExtensionText.upgradeAttention(u) },
                    trailing = if (ExtensionText.upgradeAttention(u)) ({ JTag(action, Tone.Caution) }) else null,
                    chevron = true, enabled = !busy, onClick = { onUpgrade?.invoke(u.market, u.id.ifBlank { extension.id }) })
            }
        }
    }
    note?.let { (text, n) ->
        item("upgrade-note") {
            JGroupItem(GroupPosition.of(count - 1, count)) {
                JRow(text, subtitle = n.at?.let(CatalogText::time), subtitleLines = 3,
                    trailing = if (n.kind == "rolled_back") ({ JIcon(PhIcons.WarningCircle, text, tint = PersonaTheme.palette.needsYou.dot) }) else null)
            }
        }
    }
    item("upgrade-note-footer") { Note(stringResource(ExtensionText.upgradeFooter(extension))) }
}

/** Removing an extension a market installed, at the end of its page. */
private fun LazyListScope.uninstallRow(busy: Boolean, onRemove: () -> Unit) {
    item("uninstall") {
        JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.l)) {
            JRow(stringResource(R.string.settings_ext_uninstall), leadingIcon = PhIcons.Trash, destructive = true, chevron = false, enabled = !busy, onClick = onRemove)
        }
    }
    item("uninstall-note") { Note(stringResource(R.string.settings_ext_market_installed_note)) }
}

private fun LazyListScope.form(detail: ExtensionDetail, state: ExtensionState, model: ExtensionModel, machines: List<ExtensionMachine>, onChoose: (ExtensionField, List<String>) -> Unit) {
    val plain = detail.form.filter { it.type != "object" }
    if (plain.isNotEmpty()) {
        item("form-title") { JSectionHeader(stringResource(R.string.settings_ext_settings)) }
        plain.forEach { field -> item("field-${field.name}") { ExtensionFieldBlock(field, listOf(field.name), state, model, machines, onChoose) } }
    }
    if (ExtensionDraft.paths(detail.form).isNotEmpty() && detail.form.flatMap { if (it.type == "object") it.fields.orEmpty() else listOf(it) }.any { it.items?.machines != true })
        item("form-note") { Note(stringResource(R.string.settings_ext_form_note)) }
    detail.form.filter { it.type == "object" }.forEach { group ->
        item("group-${group.name}") { JSectionHeader(group.label) }
        group.fields.orEmpty().forEach { field ->
            item("field-${group.name}.${field.name}") { ExtensionFieldBlock(field, listOf(group.name, field.name), state, model, machines, onChoose) }
        }
        group.description?.takeIf { it.isNotBlank() }?.let { item("group-note-${group.name}") { Note(it) } }
    }
}

/**
 * One field of an extension's form, by its type: a text field, a number, a switch, a choice, a list of texts or of
 * choices. Its description and the hub's word on it below.
 */
@Composable
private fun ExtensionFieldBlock(
    field: ExtensionField, path: List<String>, state: ExtensionState, model: ExtensionModel, machines: List<ExtensionMachine>,
    onChoose: (ExtensionField, List<String>) -> Unit,
) {
    val res = LocalResources.current
    val title = ExtensionText.fieldTitle(res, field)
    val error = state.fieldError(path)
    val enabled = !state.saving
    val description = field.description?.takeIf { it.isNotBlank() }
    Column(Modifier.padding(top = Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        when {
            field.type == "boolean" -> {
                val on = (state.value(path) as? JsonPrimitive)?.booleanOrNull ?: (field.fallback as? JsonPrimitive)?.booleanOrNull ?: false
                JGroupItem(GroupPosition.Single) {
                    JSwitchRow(title, on, { model.edit(path, JsonPrimitive(it)) }, subtitle = description, enabled = enabled)
                }
            }
            field.type == "array" && field.items?.machines == true -> MachineSwitches(field, title, path, state, model, machines)
            field.type == "array" -> ExtensionListField(field, title, path, state, model, enabled)
            field.enum != null -> {
                val current = state.value(path)
                val label = field.enum.orEmpty().firstOrNull { it.value == current }?.label
                    ?: (current as? JsonPrimitive)?.contentOrNull ?: ExtensionText.placeholder(res, field)
                JGroupItem(GroupPosition.Single) {
                    JRow(title, subtitle = description, value = label, enabled = enabled, onClick = { onChoose(field, path) })
                }
            }
            field.type == "integer" || field.type == "number" -> {
                val key = path.joinToString(".")
                val text = state.numberText[key] ?: (state.value(path) as? JsonPrimitive)?.contentOrNull.orEmpty()
                JTextField(text, { model.editNumber(path, it, field.type == "integer") }, Modifier.fillMaxWidth(), label = title,
                    placeholder = ExtensionText.placeholder(res, field), supportingText = error ?: description, singleLine = true, enabled = enabled,
                    isError = error != null,
                    keyboardOptions = KeyboardOptions(keyboardType = if (field.type == "integer") KeyboardType.Number else KeyboardType.Decimal))
            }
            else -> {
                if (field.name == "triage_rules" && path.size == 1) TriageTemplatePicker(enabled) { model.edit(path, JsonPrimitive(it)) }
                val text = (state.value(path) as? JsonPrimitive)?.contentOrNull.orEmpty()
                JTextField(text, { model.edit(path, it.takeIf { v -> v.isNotEmpty() }?.let(::JsonPrimitive)) }, Modifier.fillMaxWidth(),
                    label = title, placeholder = ExtensionText.placeholder(res, field), supportingText = error ?: description, singleLine = field.name != "triage_rules",
                    enabled = enabled, isError = error != null,
                    keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.None,
                        keyboardType = when (field.format) { "uri" -> KeyboardType.Uri; "email" -> KeyboardType.Email; else -> KeyboardType.Text }))
            }
        }
        // Text and number fields carry their description and error themselves (supportingText); a switch or a choice
        // has its description in its row, a list below it.
        val textField = field.type != "boolean" && field.type != "array" && field.enum == null
        if (field.type == "array") description?.takeIf { error == null }?.let { FieldSupport(it, isError = false) }
        if (!textField) error?.let { FieldSupport(it, isError = true) }
    }
}

/** A field's title above its control, set like [JTextField]'s label. */
@Composable
private fun FieldLabel(text: String) = Text(text, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = 2)

/** The line under a field, set like [JTextField]'s supporting text. */
@Composable
private fun FieldSupport(text: String, isError: Boolean) =
    Text(text, style = PersonaTheme.type.caption, color = if (isError) PersonaTheme.palette.danger.text else PersonaTheme.palette.ink3)

/**
 * A list of the hub's machines (format machine): a switch per machine of the systems the field names, by name and how it
 * is (online; for the field picking where the extension's program runs, how the program is there), in effect at once. A
 * value naming no machine still shows, to be turned off. x-confirm asks before one is turned on (turning off is not asked).
 */
@Composable
private fun MachineSwitches(field: ExtensionField, title: String, path: List<String>, state: ExtensionState, model: ExtensionModel, machines: List<ExtensionMachine>) {
    val res = LocalResources.current
    val scope = rememberCoroutineScope()
    var asking by remember(path) { mutableStateOf<MachineSwitch?>(null) }
    val selected = ExtensionMachines.selected(state, field, path)
    val rows = ExtensionMachines.switches(field, machines, selected)
    val detail = state.detail
    val picksProgram = detail?.nodeMachines == field.name
    val nodes = detail?.extension?.nodes.orEmpty()
    // An extension that operates computers (cua): a machine's row also says how Cua Driver is there.
    val computers = detail?.extension?.contributes?.containsKey("computer") == true
    fun save(id: String, on: Boolean) { scope.launch { model.saveNow(path, ExtensionMachines.toggled(selected, id, on)) } }
    FieldLabel(title)
    if (rows.isEmpty()) {
        FieldSupport(stringResource(if (field.items?.os == listOf("darwin")) R.string.settings_ext_no_mac else R.string.settings_ext_no_machine), isError = false)
        return
    }
    Column {
        rows.forEachIndexed { index, row ->
            val on = row.id in selected
            val status = if (row.known) ExtensionText.machineStatus(res, row.online, nodes.firstOrNull { it.machineId == row.id }, picksProgram, on, row.computer, computers)
                else stringResource(R.string.settings_ext_machine_not_found)
            JGroupItem(GroupPosition.of(index, rows.size)) {
                JSwitchRow(row.name, on, { next -> if (next && !field.confirm.isNullOrBlank()) asking = row else save(row.id, next) },
                    subtitle = status, leadingIcon = PhIcons.Desktop, enabled = !state.busy && !state.saving)
            }
        }
    }
    FieldSupport(stringResource(R.string.settings_ext_machines_now), isError = false)
    asking?.let { row ->
        JDialog({ asking = null }, title = stringResource(R.string.settings_ext_turn_on_title, row.name), text = field.confirm,
            confirm = stringResource(R.string.settings_ext_turn_on) to { asking = null; save(row.id, true) })
    }
}

/** A list of texts (each removable, more added below), or of choices (a switch each). */
@Composable
private fun ExtensionListField(field: ExtensionField, title: String, path: List<String>, state: ExtensionState, model: ExtensionModel, enabled: Boolean) {
    var adding by remember(path) { mutableStateOf("") }
    val items = ((state.value(path) ?: field.fallback) as? JsonArray).orEmpty().mapNotNull { (it as? JsonPrimitive)?.contentOrNull }
    fun store(list: List<String>) = model.edit(path, list.takeIf { it.isNotEmpty() }?.let { l -> JsonArray(l.map(::JsonPrimitive)) })
    fun add() {
        val value = adding.trim()
        if (value.isEmpty()) return
        store(items + value)
        adding = ""
    }
    FieldLabel(title)
    val choices = field.items?.enum
    if (choices != null) {
        Column {
            choices.forEachIndexed { index, choice ->
                val value = (choice.value as? JsonPrimitive)?.contentOrNull.orEmpty()
                JGroupItem(GroupPosition.of(index, choices.size)) {
                    JSwitchRow(choice.label, value in items, { on -> store(items.filter { it != value } + listOfNotNull(value.takeIf { on })) }, enabled = enabled)
                }
            }
        }
        return
    }
    if (items.isNotEmpty()) Column {
        items.forEachIndexed { index, item ->
            JGroupItem(GroupPosition.of(index, items.size)) {
                JRow(item, trailing = {
                    JIconButton(PhIcons.X, stringResource(R.string.settings_ext_remove_item, item), { store(items.filterIndexed { i, _ -> i != index }) }, enabled = enabled, raised = false)
                })
            }
        }
    }
    JTextField(adding, { adding = it }, Modifier.fillMaxWidth(), placeholder = stringResource(R.string.settings_ext_add_item), singleLine = true, enabled = enabled,
        keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.None, imeAction = ImeAction.Done),
        keyboardActions = KeyboardActions(onDone = { add() }),
        trailing = { JIconButton(PhIcons.Plus, stringResource(R.string.settings_ext_add), ::add, enabled = enabled && adding.isNotBlank(), raised = false) })
}

private fun LazyListScope.secrets(detail: ExtensionDetail, state: ExtensionState, onEdit: (ExtensionSecret) -> Unit) {
    item("secrets-title") { JSectionHeader(stringResource(R.string.settings_ext_secrets)) }
    itemsIndexed(detail.secrets, key = { _, it -> "secret-${it.name}" }) { index, secret ->
        JGroupItem(GroupPosition.of(index, detail.secrets.size)) {
            JRow(secret.label, leadingIcon = PhIcons.Key, value = ExtensionText.secretState(LocalResources.current, secret), enabled = !state.busy && !state.saving,
                onClick = { onEdit(secret) })
        }
    }
    item("secrets-note") { Note(stringResource(R.string.settings_ext_secrets_note)) }
}

private fun LazyListScope.contributions(
    detail: ExtensionDetail, busy: Boolean, res: Resources,
    change: (persona.core.contracts.api.ExtensionContribution, Boolean) -> Unit,
) {
    detail.contributions?.takeIf { it.isNotEmpty() }?.let { list ->
        item("contributes-title") { JSectionHeader(stringResource(R.string.settings_ext_provides)) }
        itemsIndexed(list, key = { _, it -> "point-${it.point}/${it.id}" }) { index, contribution ->
            JGroupItem(GroupPosition.of(index, list.size)) {
                if (contribution.canDisable && !detail.readOnly) JSwitchRow(
                    contribution.title?.ifBlank { null } ?: contribution.id, contribution.enabled,
                    { change(contribution, it) }, subtitle = ExtensionText.point(res, contribution.point), enabled = !busy,
                ) else JRow(contribution.title?.ifBlank { null } ?: contribution.id,
                    subtitle = res.getString(when (contribution.via) {
                        "connector" -> R.string.settings_ext_via_connector
                        "config" -> R.string.settings_ext_via_config
                        else -> R.string.settings_ext_via_read_only
                    }, ExtensionText.point(res, contribution.point)),
                    value = stringResource(if (contribution.enabled) R.string.settings_ext_enabled else R.string.settings_ext_state_off))
            }
        }
        item("contributes-note") { Note(stringResource(R.string.settings_ext_contributions_note)) }
        return
    }
    val points = detail.extension.contributes.orEmpty().filterValues { it.isNotEmpty() }.toSortedMap().toList()
    if (points.isEmpty()) return
    item("contributes-title") { JSectionHeader(stringResource(R.string.settings_ext_provides)) }
    itemsIndexed(points, key = { _, it -> "point-${it.first}" }) { index, (point, ids) ->
        JGroupItem(GroupPosition.of(index, points.size)) { JRow(ExtensionText.point(res, point), value = ids.joinToString(res.getString(R.string.settings_ext_list_separator))) }
    }
}

private fun LazyListScope.permissions(detail: ExtensionDetail) {
    if (detail.permissions.isEmpty()) return
    item("permissions-title") { JSectionHeader(stringResource(R.string.settings_ext_it_can)) }
    itemsIndexed(detail.permissions, key = { _, it -> "permission-${it.key}" }) { index, permission ->
        JGroupItem(GroupPosition.of(index, detail.permissions.size)) {
            JRow(permission.title, subtitle = permission.detail?.takeIf { it.isNotBlank() }, subtitleLines = 4)
        }
    }
    item("permissions-note") { Note(stringResource(R.string.settings_ext_permissions_note)) }
}

private fun LazyListScope.about(detail: ExtensionDetail, res: Resources) {
    val extension = detail.extension
    val rows = listOfNotNull(
        extension.version?.takeIf { it.isNotBlank() }?.let { res.getString(R.string.settings_ext_about_version) to it },
        detail.previousVersion?.takeIf { it.isNotBlank() }?.let { res.getString(R.string.settings_ext_about_previous) to it },
        ExtensionText.source(res, extension.source)?.let { res.getString(R.string.settings_ext_about_source) to it },
        detail.publisher?.takeIf { it.isNotBlank() }?.let { res.getString(R.string.settings_ext_about_publisher) to it },
    )
    val origin = detail.origin?.takeIf { it.isNotBlank() }
    val homepage = detail.homepage?.takeIf { it.startsWith("https://") || it.startsWith("http://") }
    // The command-line programs this version runs with (#4507): each with its version, the machine's own copy marked.
    val tools = extension.tools.orEmpty()
    val count = rows.size + tools.size + listOfNotNull(origin, homepage).size
    if (count == 0 && extension.warnings.isNullOrEmpty()) return
    item("about-title") { JSectionHeader(stringResource(R.string.settings_ext_about)) }
    rows.forEachIndexed { index, (label, value) ->
        item("about-$label") { JGroupItem(GroupPosition.of(index, count)) { JRow(label, value = value) } }
    }
    tools.forEachIndexed { index, tool ->
        item("about-tool-${tool.name}") {
            JGroupItem(GroupPosition.of(rows.size + index, count)) {
                val title = tool.title?.takeIf { it.isNotBlank() }
                JRow(title ?: tool.name, leadingIcon = PhIcons.TerminalWindow, value = tool.version.takeIf { it.isNotBlank() },
                    subtitle = listOfNotNull(stringResource(R.string.settings_ext_about_tools), tool.name.takeIf { title != null },
                        stringResource(R.string.settings_ext_tool_reused).takeIf { tool.reused }).joinToString(stringResource(R.string.settings_ext_dot_separator)))
            }
        }
    }
    // Where it was installed from is long (a path or an address): it goes under its label, not beside it.
    origin?.let { item("about-origin") { JGroupItem(GroupPosition.of(rows.size + tools.size, count)) { JRow(stringResource(R.string.settings_ext_about_origin), subtitle = it, subtitleLines = 3) } } }
    homepage?.let { url ->
        item("about-homepage") {
            val uri = LocalUriHandler.current
            JGroupItem(GroupPosition.of(count - 1, count)) {
                JRow(stringResource(R.string.settings_ext_about_homepage), leadingIcon = PhIcons.ArrowSquareOut, onClick = { runCatching { uri.openUri(url) } })
            }
        }
    }
    extension.warnings.orEmpty().forEachIndexed { index, warning ->
        item("warning-$index") { JInlineStatus(warning, Modifier.padding(top = Tokens.Space.s), tone = InlineTone.Caution) }
    }
}

/** Sets one of an extension's secrets: typed in hidden, saved to the hub, never shown again; one that is set can be cleared. */
@Composable
private fun ExtensionSecretSheet(
    secret: ExtensionSecret, busy: Boolean, onDismiss: () -> Unit,
    save: suspend (String) -> Boolean, clear: suspend () -> Boolean,
) {
    var value by remember(secret.name) { mutableStateOf("") }
    val scope = rememberCoroutineScope()
    JSheet(onDismiss, title = secret.label, dismissible = !busy) {
        Column(Modifier.padding(vertical = Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            JTextField(value, { value = it }, Modifier.fillMaxWidth(), placeholder = stringResource(if (secret.set) R.string.settings_ext_secret_replace else R.string.settings_ext_secret_enter), singleLine = true,
                enabled = !busy, visualTransformation = PasswordVisualTransformation(),
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password))
            SettingsCaption(secret.description?.takeIf { it.isNotBlank() } ?: stringResource(R.string.settings_ext_secret_note))
            JButton(stringResource(R.string.settings_ext_save), { scope.launch { if (save(value)) onDismiss() } }, Modifier.fillMaxWidth(), loading = busy,
                enabled = !busy && value.isNotBlank())
            if (secret.set) JButton(stringResource(R.string.settings_ext_clear), { scope.launch { if (clear()) onDismiss() } }, Modifier.fillMaxWidth(),
                kind = JButtonKind.Destructive, enabled = !busy)
        }
    }
}

/** The manager an extension stamped [manager] belongs to: its stamp, else the roster's default (none: the hub's own). */
internal fun persona.core.contracts.api.ManagerRoster?.ownerOrDefault(manager: String?): String =
    manager?.takeIf { it.isNotBlank() } ?: this?.defaultManagerId ?: persona.core.contracts.api.ManagerRoster.ASSISTANT_MANAGER

/**
 * While an extension's page is open, its requests (`extensions/<id>/…`, `plugins/<id>/…` on an older hub) name [manager]:
 * the same id may be installed under several managers (each one's built-ins, docs/assistant/managers.md), and the default's own
 * keep going to it elsewhere.
 */
@Composable
internal fun PinnedExtension(extensionId: String, manager: String?) =
    PinnedExtension(listOf(listOf("extensions", extensionId), listOf("plugins", extensionId)), manager)

/** Requests under each of [prefixes] (segments under /api/v1) name [manager] while this is shown. */
@Composable
internal fun PinnedExtension(prefixes: List<List<String>>, manager: String?) {
    DisposableEffect(prefixes, manager) {
        val release = persona.core.contracts.api.AssistantManagerChoice.pin(prefixes, manager)
        onDispose { release() }
    }
}
