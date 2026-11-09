package persona.feature.settings

import android.content.res.Resources
import persona.core.contracts.MachineComputer
import persona.core.contracts.pushManager
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.ManagerRoster
import persona.core.contracts.api.HubExtension
import persona.core.contracts.api.ExtensionConfigRejected
import persona.core.contracts.api.ExtensionAuth
import persona.core.contracts.api.ExtensionDetail
import persona.core.contracts.api.ExtensionField
import persona.core.contracts.api.ExtensionHasDependents
import persona.core.contracts.api.ExtensionManagement
import persona.core.contracts.api.ExtensionNodeState
import persona.core.contracts.api.ExtensionPermission
import persona.core.contracts.api.ExtensionUpgrade
import persona.core.contracts.api.ExtensionUpgradeNote
import persona.core.designsystem.Status
import persona.core.designsystem.failureReasonText
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.filter
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.contentOrNull

// Settings › Extensions (docs/extensions/extensions.md "App: Settings › Extensions"): the words, the draft of an extension's configuration, and the two
// pages' models. The pages are in ExtensionsScreen.kt.

/** What an extension is called and says in the settings. */
internal object ExtensionText {
    /** Running or idle until something needs it (stopped) is all one to the user: it is on. */
    fun state(res: Resources, p: HubExtension): String = when (p.status) {
        "running", "stopped" -> res.getString(R.string.settings_ext_state_on)
        "starting" -> res.getString(R.string.settings_ext_state_starting)
        "needs_setup" -> res.getString(R.string.settings_ext_state_needs_setup)
        "disabled" -> res.getString(R.string.settings_ext_state_off)
        "incompatible" -> res.getString(R.string.settings_ext_state_incompatible)
        "error" -> res.getString(R.string.settings_ext_state_error)
        else -> res.getString(R.string.settings_ext_state_unknown)
    }

    /** Whether an enabled extension's program runs right now (the detail page's small print); null for any other state. */
    fun runNote(res: Resources, p: HubExtension): String? = when {
        p.kind == "extension_pack" -> res.getString(R.string.settings_ext_run_pack)
        p.status == "running" -> res.getString(R.string.settings_ext_run_running)
        p.status == "stopped" -> res.getString(R.string.settings_ext_run_idle)
        else -> null
    }

    /** Broken: drawn in the danger colour, not as a status dot. */
    fun isProblem(p: HubExtension): Boolean = p.status == "error" || p.status == "incompatible"

    /** The dot beside the state: green on (running or idle), amber waiting on the user, blue starting, grey otherwise. */
    fun status(p: HubExtension): Status = when (p.status) {
        "running", "stopped" -> Status.Unread
        "needs_setup" -> Status.NeedsYou
        "starting" -> Status.Working
        else -> Status.Offline
    }

    /** What asking before switching off [name]'s dependents with it says (#5192); unnamed when only an operation's error_code said so (#5611). */
    fun cascade(res: Resources, ask: ExtensionCascade, name: String): String =
        if (ask.names.isEmpty()) res.getString(R.string.settings_ext_dependents_unnamed, name)
        else res.getString(R.string.settings_ext_dependents_text, ask.names.joinToString(res.getString(R.string.settings_ext_list_separator)), name)

    fun source(res: Resources, s: String?): String? = when (s) {
        "builtin" -> res.getString(R.string.settings_ext_source_builtin)
        "hub.json" -> "manager.json"
        "installed" -> res.getString(R.string.settings_ext_source_installed)
        else -> null
    }

    fun point(res: Resources, name: String): String = when (name) {
        "connectors" -> res.getString(R.string.settings_ext_point_connectors)
        "channel" -> res.getString(R.string.settings_ext_point_channel)
        "voice" -> res.getString(R.string.settings_ext_point_voice)
        "ui" -> res.getString(R.string.settings_ext_point_ui)
        "quota" -> res.getString(R.string.settings_ext_point_quota)
        "providers" -> res.getString(R.string.settings_ext_point_providers)
        "events" -> res.getString(R.string.settings_ext_point_events)
        "assistant" -> res.getString(R.string.settings_ext_point_assistant)
        "api" -> res.getString(R.string.settings_ext_point_api)
        "skills" -> res.getString(R.string.settings_ext_point_skills)
        "prompts" -> res.getString(R.string.settings_ext_point_prompts)
        "themes" -> res.getString(R.string.settings_ext_point_themes)
        "triage_templates" -> res.getString(R.string.settings_ext_point_triage_templates)
        "agent_filters" -> res.getString(R.string.settings_ext_point_agent_filters)
        "computer" -> res.getString(R.string.settings_ext_point_computer)
        "browser" -> res.getString(R.string.settings_ext_point_browser)
        "widgets" -> res.getString(R.string.settings_ext_point_widgets)
        "shortcuts" -> res.getString(R.string.settings_ext_point_shortcuts)
        else -> name
    }

    /** The detail's state line: the state, the version, where it came from. */
    fun stateLine(res: Resources, p: HubExtension): String =
        listOfNotNull(state(res, p), p.version?.takeIf { it.isNotBlank() }, source(res, p.source)).joinToString(" · ")

    /** Settings' row: how many, and how many need the user; null before the list is in. */
    fun listSummary(res: Resources, list: List<HubExtension>?): String? {
        list ?: return null
        if (list.isEmpty()) return res.getString(R.string.settings_ext_summary_none)
        val attention = list.count { it.status == "needs_setup" || isProblem(it) }
        val count = res.getQuantityString(R.plurals.settings_ext_summary_count, list.size, list.size)
        return if (attention > 0) res.getString(R.string.settings_ext_summary_attention, count,
            res.getQuantityString(R.plurals.settings_ext_summary_to_handle, attention, attention)) else count
    }

    /** A field's title, marked when it must be filled and has no default. */
    fun fieldTitle(res: Resources, field: persona.core.contracts.api.ExtensionField): String =
        if (field.required == true && field.fallback == null) res.getString(R.string.settings_ext_field_required, field.label) else field.label

    /** What an empty field shows: the default it falls back to, or "Not set". */
    fun placeholder(res: Resources, field: persona.core.contracts.api.ExtensionField): String {
        val fallback = field.fallback ?: return res.getString(R.string.settings_ext_not_set)
        val text = field.enum?.firstOrNull { it.value == fallback }?.label ?: (fallback as? JsonPrimitive)?.content
        return if (text.isNullOrEmpty()) res.getString(R.string.settings_ext_not_set) else res.getString(R.string.settings_ext_default_value, text)
    }

    fun secretState(res: Resources, secret: persona.core.contracts.api.ExtensionSecret): String = when {
        secret.set -> res.getString(R.string.settings_ext_secret_set)
        secret.required == true -> res.getString(R.string.settings_ext_secret_required)
        else -> res.getString(R.string.settings_ext_not_set)
    }

    /**
     * The list's line: its state, that a newer version waits for the user's yes (it asks for more), and that it is not
     * signed in (#4507).
     */
    fun listLine(res: Resources, p: HubExtension): String = listOfNotNull(
        state(res, p),
        res.getString(R.string.settings_ext_upgrade_pending).takeIf { p.upgrade?.state == "needs_permission" },
        res.getString(R.string.settings_ext_auth_signed_out).takeIf { p.auth?.needsSignIn == true },
    ).joinToString(res.getString(R.string.settings_ext_dot_separator))

    /** The "Upgrades" group's line under a newer version: what happens to it (the web's upgradeDetail). */
    fun upgradeDetail(res: Resources, u: ExtensionUpgrade): String = when (u.state) {
        "needs_permission" -> morePermissions(res, u.addedPermissions.orEmpty())
        "waiting_idle" -> res.getString(R.string.settings_ext_upgrade_waiting_idle)
        "available" -> res.getString(R.string.settings_ext_upgrade_available)
        "off" -> res.getString(R.string.settings_ext_upgrade_off)
        "skipped" -> res.getString(
            if (u.skippedBy == "user") R.string.settings_ext_upgrade_skipped_user else R.string.settings_ext_upgrade_skipped,
            u.from,
        )
        "failed" -> u.detail?.takeIf { it.isNotBlank() }?.let { res.getString(R.string.settings_ext_upgrade_failed_detail, it) } ?: res.getString(R.string.settings_ext_upgrade_failed)
        "incompatible" -> u.detail?.takeIf { it.isNotBlank() }?.let { res.getString(R.string.settings_ext_upgrade_incompatible_detail, it) } ?: res.getString(R.string.settings_ext_upgrade_incompatible)
        else -> ""
    }

    /** The auto-upgrade switch as shown: off while it is paused (the extension switched off), else the extension's own. */
    fun autoUpgradeShown(e: HubExtension): Boolean = e.autoUpgradePaused == null && (e.autoUpgrade ?: true)

    /**
     * The Auto-upgrade footer: why the switch does not apply now, by the hub's `auto_upgrade_paused` (docs/extensions/extensions.md);
     * a reason this app does not know says only that it is paused, not that the extension is off (#1895).
     */
    @androidx.annotation.StringRes
    fun upgradeFooter(e: HubExtension): Int = when (e.autoUpgradePaused) {
        null -> R.string.settings_ext_upgrade_footer
        "disabled" -> R.string.settings_ext_upgrade_paused_footer
        "market_removed" -> R.string.settings_ext_upgrade_market_removed_footer
        else -> R.string.settings_ext_upgrade_paused_other_footer
    }

    /** When the line asks something of the user, what its button says (it opens the upgrade in the market); null otherwise. */
    fun upgradeAction(res: Resources, u: ExtensionUpgrade): String? = when (u.state) {
        "needs_permission" -> res.getString(R.string.settings_ext_upgrade_review)
        "off", "skipped", "failed" -> res.getString(R.string.settings_ext_upgrade_go)
        else -> null
    }

    /** Whether the line is drawn as needing attention. */
    fun upgradeAttention(u: ExtensionUpgrade): Boolean = u.state == "needs_permission" || u.state == "skipped"

    /** What a newer version asks for beyond what the user agreed to. */
    fun morePermissions(res: Resources, added: List<ExtensionPermission>): String {
        if (added.isEmpty()) return res.getString(R.string.settings_ext_upgrade_permissions_changed)
        val list = added.joinToString(res.getString(R.string.settings_ext_list_separator)) { p ->
            p.detail?.takeIf { it.isNotBlank() }?.let { res.getString(R.string.settings_ext_upgrade_permission_detail, p.title, it) } ?: p.title
        }
        return res.getQuantityString(R.plurals.settings_ext_upgrade_more_permissions, added.size, added.size, list)
    }

    /** The last upgrade, when it is news of its own (asking and failing are the newer version's line). */
    fun upgradeNote(res: Resources, n: ExtensionUpgradeNote): String? = when (n.kind) {
        "upgraded" -> res.getString(R.string.settings_ext_upgraded_note, n.to, n.from)
        "rolled_back" -> res.getString(R.string.settings_ext_rolled_back_note, n.to, n.from)
        else -> null
    }

    /**
     * A machine of a machine list (format machine): online or not; for the field picking where the program runs, how it
     * is there; otherwise, for an extension that operates computers ([computers]), how Cua Driver is there once its node
     * has said (the web's machineStatus).
     */
    fun machineStatus(
        res: Resources, online: Boolean, program: ExtensionNodeState?, picksProgram: Boolean, on: Boolean,
        computer: MachineComputer? = null, computers: Boolean = false,
    ): String {
        val presence = res.getString(if (online) R.string.settings_ext_machine_online else R.string.settings_ext_machine_offline)
        val separator = res.getString(R.string.settings_ext_dot_separator)
        if (picksProgram && on) return listOf(presence, programStatus(res, program)).joinToString(separator)
        val cua = computer?.takeIf { computers && online && (it.ready || !it.note.isNullOrBlank()) }?.let { cuaStatus(res, it) }
        return listOfNotNull(presence, cua).joinToString(separator)
    }

    /** How Cua Driver is on a Mac, the first thing missing first. */
    fun cuaStatus(res: Resources, c: MachineComputer): String = res.getString(when {
        !c.installed -> R.string.settings_ext_cua_missing
        !c.daemon -> R.string.settings_ext_cua_not_running
        c.accessibility == "missing" || c.screenRecording == "missing" -> R.string.settings_ext_cua_no_permission
        c.desktop == "locked" -> R.string.settings_ext_screen_locked
        c.ready -> R.string.settings_ext_cua_ready
        else -> R.string.settings_ext_cua_installed
    })

    /** How an extension's program is on a machine switched on for it. */
    fun programStatus(res: Resources, s: ExtensionNodeState?): String {
        s ?: return res.getString(R.string.settings_ext_program_waiting)
        val error = s.error?.takeIf { it.isNotBlank() }
        return when (s.state) {
            "running" -> s.version?.takeIf { it.isNotBlank() }?.let { res.getString(R.string.settings_ext_program_running_version, it) } ?: res.getString(R.string.settings_ext_program_running)
            "starting" -> res.getString(R.string.settings_ext_program_starting)
            "missing" -> res.getString(R.string.settings_ext_program_sending)
            "offline" -> res.getString(R.string.settings_ext_program_offline)
            "lease_expired" -> res.getString(R.string.settings_ext_program_lease_expired)
            "outdated" -> res.getString(R.string.settings_ext_program_outdated)
            "error" -> error?.let { res.getString(R.string.settings_ext_program_error_detail, it) } ?: res.getString(R.string.settings_ext_program_error)
            else -> listOfNotNull(s.state, error).joinToString(res.getString(R.string.settings_ext_colon))
        }
    }
}

/** A machine as a machine list draws it: only what its row shows, so live metrics do not redraw the page. */
internal data class ExtensionMachine(val id: String, val name: String, val os: String?, val online: Boolean, val computer: MachineComputer? = null)

/** One switch of a machine list: a machine of the systems the field names, or a value naming no machine (still shown, to turn off). */
internal data class MachineSwitch(val id: String, val name: String, val known: Boolean, val online: Boolean, val computer: MachineComputer? = null)

internal object ExtensionMachines {
    fun selected(state: ExtensionState, field: ExtensionField, path: List<String>): List<String> =
        ((state.value(path) ?: field.fallback) as? JsonArray).orEmpty().mapNotNull { (it as? JsonPrimitive)?.contentOrNull }

    fun switches(field: ExtensionField, machines: List<ExtensionMachine>, selected: List<String>): List<MachineSwitch> {
        val os = field.items?.os.orEmpty()
        val shown = machines.filter { os.isEmpty() || it.os in os }
        val unknown = selected.filter { id -> machines.none { it.id == id } }
        return shown.map { MachineSwitch(it.id, it.name.ifBlank { it.id }, known = true, online = it.online, computer = it.computer) } +
            unknown.map { MachineSwitch(it, it, known = false, online = false) }
    }

    /** The list after turning one machine on or off; empty is "not set". */
    fun toggled(selected: List<String>, id: String, on: Boolean): JsonArray? =
        (selected.filter { it != id } + listOfNotNull(id.takeIf { on })).takeIf { it.isNotEmpty() }?.let { JsonArray(it.map(::JsonPrimitive)) }
}

/** The configuration being edited, by dotted path ("proxy.host"); an empty group goes with its last value. */
internal object ExtensionDraft {
    fun value(draft: JsonObject, path: List<String>): JsonElement? {
        var current: JsonElement = draft
        for (key in path) current = (current as? JsonObject)?.get(key) ?: return null
        return current.takeIf { it != JsonNull }
    }

    fun set(draft: JsonObject, path: List<String>, value: JsonElement?): JsonObject {
        val key = path.firstOrNull() ?: return draft
        val out = draft.toMutableMap()
        val next = if (path.size == 1) value else set(draft[key] as? JsonObject ?: JsonObject(emptyMap()), path.drop(1), value).takeIf { it.isNotEmpty() }
        if (next == null) out.remove(key) else out[key] = next
        return JsonObject(out)
    }

    /** Without empty strings and lists the user cleared: "not set" (the schema's default applies). */
    fun compact(obj: JsonObject): JsonObject = JsonObject(obj.mapNotNull { (key, value) ->
        when {
            value == JsonNull -> null
            value is JsonPrimitive && value.isString && value.content.isEmpty() -> null
            value is JsonArray && value.isEmpty() -> null
            value is JsonObject -> compact(value).takeIf { it.isNotEmpty() }?.let { key to it }
            else -> key to value
        }
    }.toMap())

    /** The dotted path of every field the form draws (an object's fields under its name). */
    fun paths(form: List<persona.core.contracts.api.ExtensionField>): Set<String> = form.flatMap { field ->
        if (field.type == "object") field.fields.orEmpty().map { "${field.name}.${it.name}" } else listOf(field.name)
    }.toSet()

    /** A number as typed: its value, or why it is not one (blank is "not set"). */
    fun number(res: Resources, text: String, integer: Boolean): Pair<JsonPrimitive?, String?> {
        val t = text.trim()
        if (t.isEmpty()) return null to null
        val n = t.toDoubleOrNull()?.takeIf { it.isFinite() } ?: return null to res.getString(if (integer) R.string.settings_ext_number_integer else R.string.settings_ext_number_any)
        val whole = n == Math.floor(n) && kotlin.math.abs(n) < 1e15
        if (integer && !whole) return null to res.getString(R.string.settings_ext_number_integer)
        return (if (whole) JsonPrimitive(n.toLong()) else JsonPrimitive(n)) to null
    }
}

internal data class ExtensionListState(
    val extensions: List<HubExtension>? = null,
    /** The hub has extension markets (`markets: true`): the list opens "Extension market". */
    val markets: Boolean = false,
    val loading: Boolean = false,
    val error: String? = null,
)

/**
 * An extension's page changed something the list shows (removed it, switched it, its auto-upgrade): in a wide window the
 * list stays beside the page, so it reloads on this instead of waiting for a pull. By hub connection, so another hub's
 * page does not reload this one.
 */
internal object ExtensionChanges {
    private val changes = MutableSharedFlow<ExtensionManagement>(extraBufferCapacity = 8)

    fun changed(api: ExtensionManagement) { changes.tryEmit(api) }

    fun of(api: ExtensionManagement): Flow<Unit> = changes.filter { it === api }.map { }
}

/**
 * Whose an extension, a page or a list's item is: the manager it names, else the default manager (the hub answers what
 * names none from it). Compared with a push's [pushManager] (#1834).
 */
internal object ExtensionOwners {
    fun of(manager: String?): String = manager?.takeIf { it.isNotBlank() } ?: default
    val default: String get() = AssistantManagerChoice.current.value.defaultId.ifBlank { ManagerRoster.ASSISTANT_MANAGER }
}

/** Settings › Extensions: the list. */
internal class ExtensionListModel(private val api: ExtensionManagement, private val res: Resources) {
    private val mutable = MutableStateFlow(ExtensionListState())
    val state = mutable.asStateFlow()

    suspend fun load() {
        if (state.value.loading) return
        mutable.update { it.copy(loading = true) }
        try {
            val listing = api.listing()
            mutable.update { it.copy(extensions = listing.extensions, markets = listing.markets, error = null) }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) { mutable.update { it.copy(error = res.getString(R.string.settings_ext_load_failed, failureReasonText(error).resolve(res))) } }
        finally { mutable.update { it.copy(loading = false) } }
    }

    /** Follows the hub's `extension_auth` pushes while the list is open: each changes its own manager's copy (#1834). */
    suspend fun watchAuth() = api.authUpdates.collect { event ->
        mutable.update { s ->
            s.copy(extensions = s.extensions?.map {
                if (it.id == event.extension && ExtensionOwners.of(it.manager) == pushManager(event.manager)) it.copy(auth = event.auth) else it
            })
        }
    }
}

internal data class ExtensionState(
    val detail: ExtensionDetail? = null,
    /** The configuration being edited; saved as a whole (PUT replaces it). */
    val draft: JsonObject = JsonObject(emptyMap()),
    /** What number fields hold as typed, by path, until saved. */
    val numberText: Map<String, String> = emptyMap(),
    /** Number fields typed into that do not read as one, by path. */
    val numberProblems: Map<String, String> = emptyMap(),
    /** The hub's word on a field it refused, by its dotted path. */
    val fieldErrors: Map<String, String> = emptyMap(),
    val loading: Boolean = false,
    val saving: Boolean = false,
    /** A switch or secret call is on its way. */
    val busy: Boolean = false,
    val error: String? = null,
    /** The hub would switch others off with it (409 `has_dependents`): asked before going on with `cascade`. */
    val cascade: ExtensionCascade? = null,
) {
    val editable: Boolean get() = detail?.readOnly == false
    val dirty: Boolean get() = detail != null && (draft != detail.config || numberProblems.isNotEmpty())
    val canSave: Boolean get() = dirty && numberProblems.isEmpty() && !saving && !busy

    fun value(path: List<String>): JsonElement? = ExtensionDraft.value(draft, path)

    fun fieldError(path: List<String>): String? = path.joinToString(".").let { fieldErrors[it] ?: numberProblems[it] }
}

/** The dependents named by `has_dependents`; [keepData] is the removal asked about, null switching it off. */
internal data class ExtensionCascade(val names: List<String>, val keepData: Boolean? = null)

/** Settings › Extensions › one: its switch, its form (a draft saved as a whole), its secrets. */
internal class ExtensionModel(
    private val api: ExtensionManagement, private val id: String, private val res: Resources,
    /** Whose extension the page shows (several managers); null: the default's. */
    private val manager: String? = null,
) {
    private val mutable = MutableStateFlow(ExtensionState())
    val state = mutable.asStateFlow()

    suspend fun load() {
        if (state.value.loading) return
        mutable.update { it.copy(loading = true) }
        try { run { api.extension(id) } } finally { mutable.update { it.copy(loading = false) } }
    }

    suspend fun setEnabled(on: Boolean, cascade: Boolean = false) = busy { run { api.setEnabled(id, on, cascade) } }

    /** Asked about the dependents: switches them off with it and goes on with what was refused. */
    suspend fun confirmCascade(): Boolean {
        val ask = state.value.cascade ?: return false
        mutable.update { it.copy(cascade = null) }
        return if (ask.keepData == null) setEnabled(false, cascade = true) else uninstall(ask.keepData, cascade = true)
    }

    fun dismissCascade() = mutable.update { it.copy(cascade = null) }

    /** Follows the hub's `extension_auth` pushes for this extension while the page is open (#4507), its manager's only (#1834). */
    suspend fun watchAuth() = api.authUpdates.collect { event ->
        if (event.extension == id && ExtensionOwners.of(manager) == pushManager(event.manager)) setAuth(event.auth)
    }

    /** The sign-in as the hub says it is now (a push, signing out). */
    fun setAuth(auth: ExtensionAuth) = mutable.update { s ->
        s.copy(detail = s.detail?.let { d -> d.copy(extension = d.extension.copy(auth = auth)) })
    }

    suspend fun setContribution(item: persona.core.contracts.api.ExtensionContribution, on: Boolean) = busy {
        run { api.setContribution(id, item.point, item.id, on) }
    }

    fun edit(path: List<String>, value: JsonElement?) = mutable.update {
        val key = path.joinToString(".")
        it.copy(draft = ExtensionDraft.set(it.draft, path, value), fieldErrors = it.fieldErrors - key)
    }

    /** A number field as typed: kept as text, its value put in the draft once it reads as one. */
    fun editNumber(path: List<String>, text: String, integer: Boolean) = mutable.update {
        val key = path.joinToString(".")
        val (number, problem) = ExtensionDraft.number(res, text, integer)
        it.copy(
            draft = if (problem == null) ExtensionDraft.set(it.draft, path, number) else it.draft,
            numberText = it.numberText + (key to text),
            numberProblems = if (problem == null) it.numberProblems - key else it.numberProblems + (key to problem),
            fieldErrors = it.fieldErrors - key,
        )
    }

    /** Saves the whole draft; a field the hub refuses keeps the draft and shows beside that field. */
    suspend fun save(): Boolean {
        if (!state.value.canSave) return false
        mutable.update { it.copy(saving = true, fieldErrors = emptyMap(), error = null) }
        return try {
            run(saved = true) { api.setConfig(id, ExtensionDraft.compact(state.value.draft)) }.also { if (it) ExtensionChanges.changed(api) }
        } finally { mutable.update { it.copy(saving = false) } }
    }

    suspend fun setSecret(name: String, value: String): Boolean {
        val trimmed = value.trim()
        if (trimmed.isEmpty()) return false
        return busy { run { api.setSecret(id, name, trimmed) } }
    }

    suspend fun clearSecret(name: String): Boolean = busy { run { api.clearSecret(id, name) } }

    /** A market extension's own auto-upgrade switch (docs/extensions/extensions.md "Auto-upgrade"). */
    suspend fun setAutoUpgrade(on: Boolean): Boolean = busy {
        run(failed = if (on) R.string.settings_ext_auto_upgrade_on_failed else R.string.settings_ext_auto_upgrade_off_failed) { api.setAutoUpgrade(id, on) }
    }

    /**
     * A switch that takes effect at once (a machine of a machine list): the saved configuration with this one value
     * changed goes to the hub; any other edit stays in the draft, unsaved. Refused, the switch goes back.
     */
    suspend fun saveNow(path: List<String>, value: JsonElement?): Boolean {
        val saved = state.value.detail?.config ?: return false
        if (state.value.busy || state.value.saving) return false
        mutable.update { it.copy(busy = true, error = null, draft = ExtensionDraft.set(it.draft, path, value)) }
        return try {
            val detail = api.setConfig(id, ExtensionDraft.compact(ExtensionDraft.set(saved, path, value)))
            mutable.update { it.copy(detail = detail, draft = ExtensionDraft.set(it.draft, path, ExtensionDraft.value(detail.config, path))) }
            ExtensionChanges.changed(api)
            true
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            val reason = (error as? ExtensionConfigRejected)?.message ?: failureReasonText(error).resolve(res)
            mutable.update { it.copy(draft = ExtensionDraft.set(it.draft, path, ExtensionDraft.value(saved, path)),
                error = res.getString(R.string.settings_ext_not_saved, reason)) }
            false
        } finally { mutable.update { it.copy(busy = false) } }
    }

    /** Removes an extension installed from a market; its data folder stays when [keepData]. */
    suspend fun uninstall(keepData: Boolean, cascade: Boolean = false): Boolean {
        if (state.value.busy || state.value.saving) return false
        mutable.update { it.copy(busy = true, error = null) }
        return try {
            api.uninstall(id, keepData, cascade)
            ExtensionChanges.changed(api)
            true
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (refused: ExtensionHasDependents) {
            mutable.update { it.copy(cascade = ExtensionCascade(refused.dependents.map { d -> d.label }, keepData)) }
            false
        } catch (error: Exception) {
            mutable.update { it.copy(error = res.getString(R.string.settings_ext_not_removed, failureReasonText(error).resolve(res))) }
            false
        } finally { mutable.update { it.copy(busy = false) } }
    }

    private suspend fun busy(block: suspend () -> Boolean): Boolean {
        if (state.value.busy || state.value.saving) return false
        mutable.update { it.copy(busy = true) }
        return try { block().also { if (it) ExtensionChanges.changed(api) } } finally { mutable.update { it.copy(busy = false) } }
    }

    /**
     * Puts the hub's answer in. The draft follows it after a save, or while it holds no edit; an edit the user has
     * not saved survives switching the extension or setting a secret. [failed] words a failure of its own (one argument,
     * the reason) in place of the generic one.
     */
    private suspend fun run(saved: Boolean = false, failed: Int? = null, call: suspend () -> ExtensionDetail): Boolean = try {
        val detail = call()
        mutable.update {
            val follow = saved || !it.dirty
            it.copy(
                detail = detail, error = null,
                draft = if (follow) detail.config else it.draft,
                numberText = if (follow) emptyMap() else it.numberText,
                numberProblems = if (follow) emptyMap() else it.numberProblems,
            )
        }
        true
    } catch (cancelled: CancellationException) { throw cancelled }
    catch (refused: ExtensionConfigRejected) {
        val reason = refused.message ?: res.getString(R.string.settings_ext_config_wrong)
        // A field the form does not draw (an older form, a whole-object rule) has nowhere to show: it goes on top.
        val drawn = refused.field in ExtensionDraft.paths(state.value.detail?.form.orEmpty())
        mutable.update {
            if (drawn) it.copy(fieldErrors = mapOf(refused.field to reason), error = null)
            else it.copy(fieldErrors = emptyMap(), error = res.getString(R.string.settings_ext_not_saved,
                listOf(refused.field, reason).filter { f -> f.isNotBlank() }.joinToString(res.getString(R.string.settings_ext_colon))))
        }
        false
    } catch (refused: ExtensionHasDependents) {
        // Only switching it off is refused so; asked, it goes on with cascade.
        mutable.update { it.copy(cascade = ExtensionCascade(refused.dependents.map { d -> d.label }), error = null) }
        false
    } catch (error: Exception) {
        mutable.update { it.copy(error = res.getString(failed ?: if (it.detail == null) R.string.settings_ext_load_failed else R.string.settings_ext_not_changed, failureReasonText(error).resolve(res))) }
        false
    }
}
