package persona.android.di

import persona.android.AppText
import persona.android.R
import dagger.Module
import dagger.Provides
import dagger.hilt.InstallIn
import dagger.hilt.components.SingletonComponent
import persona.core.contracts.GroupKeys
import persona.core.contracts.HubUpdateEvent
import persona.core.contracts.ManagersEvent
import persona.core.contracts.ExtensionUiEvent
import persona.core.contracts.ExtensionOperationEvent
import persona.core.contracts.ExtensionAuthEvent
import persona.core.contracts.Hello
import persona.core.contracts.ServerEvent
import persona.core.contracts.pushManager
import persona.core.contracts.api.*
import persona.core.hub.HttpConnectorsManagement
import persona.core.hub.HttpComputerPairing
import persona.core.hub.HttpHubUpdates
import persona.core.hub.HttpHubManagers
import persona.core.hub.HttpHubSent
import persona.core.hub.HttpMachineManagement
import persona.core.hub.HttpAgentTriage
import persona.core.hub.HttpModelConfiguration
import persona.core.hub.HttpExtensionManagement
import persona.core.hub.HttpProviderSetup
import persona.core.hub.HubError
import persona.core.hub.HubFailure
import persona.core.hub.OkHttpHubConnection
import persona.core.hub.extensionStreamBindings
import android.app.Application
import persona.android.PersonaApp
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.emptyFlow
import kotlinx.coroutines.flow.filterIsInstance
import kotlinx.coroutines.flow.flatMapLatest
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.mapLatest
import kotlinx.coroutines.flow.filter
import kotlinx.coroutines.flow.transform
import kotlinx.serialization.json.JsonObject
import javax.inject.Singleton

/**
 * Machine management (machines page, engine, Claude Code settings), model configuration, extensions and hub version. Machine management goes to the hub group of the
 * machine it names; model configuration and extensions to the selected group. Either way the connection waits for its route, adds the device token
 * and drops calls of a pairing that has gone.
 */
@Module
@InstallIn(SingletonComponent::class)
object MachineManagementModule {
    private fun ConnectionRuntime.connection(): OkHttpHubConnection {
        val data = scopedData.value ?: throw HubFailure(HubError.NoNetwork, AppText.get(R.string.app_error_pair_first), retryable = false)
        return connectionFor(data)
    }

    // The machine ids come from the merged lists: each call goes to the group the id names, on that group's connection.
    // Subscription quota belongs to a hub: every open group's, each on its own hub, as the overview lists every group's machines.
    @Provides @Singleton fun machines(stacks: GroupStacks): MachineManagement = mergedMachineManagement(object : MachineRoutes {
        override suspend fun <T> on(machineId: String, call: suspend (MachineManagement) -> T): T =
            stacks.use(GroupKeys.namespaceOf(machineId)) { call(HttpMachineManagement(it.connection).inGroup(it.names)) }
    }, usage = {
        stacks.stacks.value.map { stack ->
            GroupUsageRead(stack.namespace.orEmpty()) { stacks.use(stack.namespace) { HttpMachineManagement(it.connection).usageWindows() } }
        }
    })

    // A hub-level page: the selected group's hub, whose own plain ids it reads and sends back (docs/android/android.md "Multiple hubs").
    // Bound to the pairing at each call, like model configuration: switching hubs reads the new hub's switches.
    @Provides @Singleton fun triage(runtime: ConnectionRuntime): AgentTriage = object : AgentTriage {
        override suspend fun switches() = HttpAgentTriage(runtime.connection()).switches()
        override suspend fun put(value: TriageSwitches) = HttpAgentTriage(runtime.connection()).put(value)
    }

    @Provides @Singleton fun models(runtime: ConnectionRuntime): ModelConfiguration = object : ModelConfiguration {
        private fun api() = HttpModelConfiguration(runtime.connection())
        override suspend fun catalog() = api().catalog()
        override suspend fun purpose(id: String) = api().purpose(id)
        override suspend fun save(purpose: ModelPurpose, draft: ModelConfigurationDraft) = api().save(purpose, draft)
        override suspend fun providerModels(provider: String, refresh: Boolean, machine: String?) = api().providerModels(provider, refresh, machine)
    }

    // The setup checklist's main-conversation steps: the selected group's hub, while its hello says assistant_readiness.
    @OptIn(ExperimentalCoroutinesApi::class)
    @Provides @Singleton fun readiness(runtime: ConnectionRuntime, application: Application): AssistantReadinessSource = object : AssistantReadinessSource {
        override val available: Flow<Boolean> = (application as PersonaApp).hub
            .flatMapLatest { hub -> hub?.connection?.serverFeatures ?: flowOf(emptySet()) }
            .map { AssistantReadinessFeature in it }.distinctUntilChanged()
        private fun api(): AssistantManagement {
            val connection = runtime.connection()
            val route = connection.route.value ?: throw HubFailure(HubError.NoNetwork)
            return persona.core.hub.HttpAssistantManagement(route.endpoint.url, connection.token, connection.http.client) {
                connection.route.value === route
            }
        }
        override suspend fun readiness(probe: Boolean) = api().readiness(probe)
        override suspend fun dismiss(dismissed: Boolean) = api().dismissReadiness(dismissed)
    }

    // "Add model provider" from model configuration: the selected group's hub keeps the provider.
    @OptIn(ExperimentalCoroutinesApi::class)
    @Provides @Singleton fun providers(runtime: ConnectionRuntime, application: Application): ProviderSetup = object : ProviderSetup {
        private fun api() = HttpProviderSetup(runtime.connection())
        override val builtinPresets: Flow<Boolean> = (application as PersonaApp).hub
            .flatMapLatest { hub -> hub?.connection?.serverFeatures ?: flowOf(emptySet()) }
            .map { BuiltinPresetsFeature in it }.distinctUntilChanged()
        override suspend fun presets() = api().presets()
        override suspend fun add(provider: NewProvider) = api().add(provider)
        override suspend fun create(provider: NewProvider) = api().create(provider)
        override suspend fun test(probe: ProviderProbe) = api().test(probe)
        override suspend fun discover(probe: ProviderProbe) = api().discover(probe)
        override suspend fun launchers() = api().launchers()
        override suspend fun launcher(name: String) = api().launcher(name)
        override suspend fun remove(name: String) = api().remove(name)
        override suspend fun restore(name: String) = api().restore(name)
        override suspend fun check(name: String, machine: String?) = api().check(name, machine)
        override suspend fun importPreview(machine: String) = api().importPreview(machine)
        override suspend fun import(machine: String, providers: List<String>) = api().import(machine, providers)
    }

    // Machines › "Add computer" (docs/assistant/onboarding.md 4.2): a new computer on the selected group's hub; a code again, the machine list
    // and confirm / reject on the hub the machine's id names, with its plain id (the answers carry the merged one).
    @OptIn(ExperimentalCoroutinesApi::class)
    @Provides @Singleton fun computers(stacks: GroupStacks): ComputerPairing = object : ComputerPairing {
        override val canAdd: Flow<Boolean> = stacks.selected
            .flatMapLatest { stack -> stack?.connection?.serverFeatures ?: flowOf(emptySet()) }
            .map { MachinesAddFeature in it }.distinctUntilChanged()
        private fun merged(code: ComputerCode, namespace: String?) = code.copy(machine = GroupKeys.machine(code.machine, namespace))
        override suspend fun add(name: String, os: ComputerOs): ComputerCode {
            val stack = stacks.selected.value ?: throw HubFailure(HubError.NoNetwork, AppText.get(R.string.app_error_pair_first), retryable = false)
            return stacks.use(stack.namespace) { merged(HttpComputerPairing(it.connection).add(name, os), it.namespace) }
        }
        override suspend fun again(machineId: String) = stacks.use(GroupKeys.namespaceOf(machineId)) {
            merged(HttpComputerPairing(it.connection).again(GroupKeys.raw(machineId)), it.namespace)
        }
        override suspend fun machines(machineId: String) = stacks.use(GroupKeys.namespaceOf(machineId)) { stack ->
            HttpComputerPairing(stack.connection).machines().map { it.copy(id = GroupKeys.machine(it.id, stack.namespace)) }
        }
        override suspend fun confirm(machineId: String) = stacks.use(GroupKeys.namespaceOf(machineId)) {
            HttpComputerPairing(it.connection).confirm(GroupKeys.raw(machineId))
        }
        override suspend fun reject(machineId: String) = stacks.use(GroupKeys.namespaceOf(machineId)) {
            HttpComputerPairing(it.connection).reject(GroupKeys.raw(machineId))
        }
    }

    // A hub-level page too: the selected group's extensions.
    @OptIn(ExperimentalCoroutinesApi::class)
    @Provides @Singleton fun extensions(runtime: ConnectionRuntime, application: Application): ExtensionManagement = object : ExtensionManagement {
        // The selected hub's features, from its hello: the settings row shows while it says extension_manager
        // (plugin_manager before #1844).
        private val features = (application as PersonaApp).hub
            .flatMapLatest { hub -> hub?.connection?.serverFeatures ?: flowOf(emptySet()) }
        override val available: Flow<Boolean> = features.map { "extension_manager" in it || "plugin_manager" in it }.distinctUntilChanged()
        // Extensions' UI slots (docs/extensions/extensions.md "UI slots") and the badge events of the selected hub.
        override val slotsAvailable: Flow<Boolean> = features.map { "extension_ui_slots" in it || "plugin_ui_slots" in it }.distinctUntilChanged()
        override val nativeStream = runtime.selected.flatMapLatest { group ->
            val connection = group?.connectionOrNull
            if (group == null || connection == null || group.data == null) flowOf(null) else extensionStreamBindings(connection) {
                val data = checkNotNull(group.data)
                "${group.id}:${data.scope.hubId}:${data.scope.pairingGeneration}" to (group.namespace ?: "primary")
            }
        }
        override val uiEvents: Flow<ExtensionUiEvent> = (application as PersonaApp).hub
            .flatMapLatest { hub -> hub?.connection?.events ?: emptyFlow() }.filterIsInstance<ExtensionUiEvent>()
        // A hub without the `extensions` feature (before #1844, or no hello yet) answers under /api/v1/plugins only;
        // a newer one under both.
        private fun api() = runtime.connection().let { HttpExtensionManagement(it, available, legacy = "extensions" !in it.serverFeatures.value) }
        private val updates = kotlinx.coroutines.flow.MutableSharedFlow<Unit>(extraBufferCapacity = 1)
        // Installs in steps (docs/protocol/protocol.md "Extension install, upgrade and uninstall progress"): the hub's pushes, and
        // null to list them again ([extensionOperationUpdates]).
        override val operationsAvailable: Flow<Boolean> = features.map { "extension_operations" in it }.distinctUntilChanged()
        override val operationUpdates: Flow<ExtensionOperation?> = extensionOperationUpdates((application as PersonaApp).hub
            .flatMapLatest { hub -> hub?.connection?.events ?: emptyFlow() })
        // One that ended well changed the list as a synchronous call would have.
        override val changes: Flow<Unit> = kotlinx.coroutines.flow.merge(updates,
            operationUpdates.filter { it?.state == ExtensionOperation.Succeeded }.map { })
        private suspend fun <T> changed(block: suspend () -> T): T = block().also { updates.tryEmit(Unit) }
        override suspend fun quickPrompts() = api().quickPrompts()
        override suspend fun themes() = api().themes()
        override suspend fun triageTemplates() = api().triageTemplates()
        override suspend fun setContribution(id: String, point: String, contribution: String, enabled: Boolean) =
            changed { api().setContribution(id, point, contribution, enabled) }
        override suspend fun extensions() = api().extensions()
        override suspend fun extension(id: String) = api().extension(id)
        override suspend fun setEnabled(id: String, enabled: Boolean, cascade: Boolean) = changed { api().setEnabled(id, enabled, cascade) }
        override suspend fun setConfig(id: String, config: JsonObject) = api().setConfig(id, config)
        override suspend fun setSecret(id: String, name: String, value: String) = api().setSecret(id, name, value)
        override suspend fun clearSecret(id: String, name: String) = api().clearSecret(id, name)
        override suspend fun listing() = api().listing()
        override suspend fun uiPlaces(placement: String, launcher: String?, machine: String?) = api().uiPlaces(placement, launcher, machine)
        override suspend fun renderUi(extension: String, ui: String, context: ExtensionUiContext?, state: String?) =
            api().renderUi(extension, ui, context, state)
        override suspend fun uiAction(
            extension: String, ui: String, action: String, context: ExtensionUiContext?, state: String?, values: Map<String, String>?,
        ) = api().uiAction(extension, ui, action, context, state, values)
        override suspend fun markets() = api().markets()
        override suspend fun addMarket(spec: String, token: String?) = api().addMarket(spec, token)
        override suspend fun removeMarket(id: String) = api().removeMarket(id)
        override suspend fun refreshMarket(id: String) = api().refreshMarket(id)
        override suspend fun setMarketToken(id: String, token: String) = api().setMarketToken(id, token)
        override suspend fun catalog(query: String, market: String?, refresh: Boolean) = api().catalog(query, market, refresh)
        override suspend fun previewFromMarket(market: String, extension: String) = api().previewFromMarket(market, extension)
        override suspend fun install(preview: ExtensionPreview) = changed { api().install(preview) }
        override suspend fun upgrade(id: String, preview: ExtensionPreview) = changed { api().upgrade(id, preview) }
        override suspend fun uninstall(id: String, keepData: Boolean, cascade: Boolean) = changed { api().uninstall(id, keepData, cascade) }
        override suspend fun startFromMarket(market: String, extension: String) = api().startFromMarket(market, extension)
        override suspend fun agree(preview: ExtensionPreview, upgradeOf: String?) = api().agree(preview, upgradeOf)
        override suspend fun startUninstall(id: String, keepData: Boolean, cascade: Boolean) = api().startUninstall(id, keepData, cascade)
        override suspend fun operations() = api().operations()
        override suspend fun operation(id: String) = api().operation(id)
        override suspend fun retryOperation(id: String) = api().retryOperation(id)
        override suspend fun cancelOperation(id: String) = api().cancelOperation(id)
        // Signing in (#4507): the selected hub's pushes; a finished sign-in changes the list's line too.
        override val authAvailable: Flow<Boolean> = features.map { "extension_auth" in it }.distinctUntilChanged()
        override val authUpdates: Flow<ExtensionAuthEvent> = (application as PersonaApp).hub
            .flatMapLatest { hub -> hub?.connection?.events ?: emptyFlow() }.filterIsInstance<ExtensionAuthEvent>()
        override suspend fun auth(id: String) = api().auth(id)
        override suspend fun authStep(id: String, step: String, values: Map<String, String>) = api().authStep(id, step, values)
        override suspend fun authLogout(id: String) = api().authLogout(id)
    }

    // Settings › Hub version (docs/operations/hub-update.md "In the app"): the selected group's hub, following its `hub_update` pushes.
    @OptIn(ExperimentalCoroutinesApi::class)
    @Provides @Singleton fun hubUpdates(runtime: ConnectionRuntime, application: Application): HubUpdates = object : HubUpdates {
        override val events: Flow<HubUpdateStatus> = (application as PersonaApp).hub
            .flatMapLatest { hub -> hub?.connection?.events ?: emptyFlow() }.filterIsInstance<HubUpdateEvent>().map { it.update }
        private fun api() = HttpHubUpdates(runtime.connection())
        override suspend fun status() = api().status()
        override suspend fun check() = api().check()
        override suspend fun start(now: Boolean) = api().start(now)
        override suspend fun settings(auto: Boolean?, noticeSeen: Boolean?) = api().settings(auto, noticeSeen)
        override suspend fun history() = api().history()
    }

    // Settings › Managers (docs/assistant/managers.md): the selected group's hub, reading the list again on its `managers` pushes.
    @OptIn(ExperimentalCoroutinesApi::class)
    @Provides @Singleton fun managers(runtime: ConnectionRuntime, application: Application): HubManagers = object : HubManagers {
        override val changes: Flow<Unit> = (application as PersonaApp).hub
            .flatMapLatest { hub -> hub?.connection?.events ?: emptyFlow() }.filterIsInstance<ManagersEvent>().map { }
        private fun api() = HttpHubManagers(runtime.connection())
        override suspend fun roster() = api().roster()
        override suspend fun create(name: String) = api().create(name)
        override suspend fun newCode(id: String) = api().newCode(id)
        override suspend fun rename(id: String, name: String) = api().rename(id, name)
        override suspend fun makeDefault(id: String) = api().makeDefault(id)
        override suspend fun delete(id: String) = api().delete(id)
    }

    // Settings › Sent as you (docs/extensions/connectors.md "Sent as the user"): the selected group's hub, its rows' machine ids in the group's form.
    @Provides @Singleton fun sent(runtime: ConnectionRuntime): HubSent = SelectedGroupSent {
        val data = runtime.scopedData.value ?: throw HubFailure(HubError.NoNetwork, AppText.get(R.string.app_error_pair_first), retryable = false)
        SelectedGroupSent.Group(HttpHubSent(runtime.connectionFor(data)), runtime.groups.value.firstOrNull { it.data === data }?.namespace)
    }

    // A hub-level page: the selected group's connectors (docs/extensions/connectors.md).
    @Provides @Singleton fun connectors(runtime: ConnectionRuntime): ConnectorsManagement = object : ConnectorsManagement {
        private fun api() = HttpConnectorsManagement(runtime.connection())
        override suspend fun connectors(all: Boolean) = api().connectors(all)
        override suspend fun setConnectorEnabled(id: String, enabled: Boolean) = api().setConnectorEnabled(id, enabled)
        override suspend fun setConnectorFeed(id: String, feed: String, on: Boolean) = api().setConnectorFeed(id, feed, on)
        override suspend fun deleteConnectorData(id: String) = api().deleteConnectorData(id)
        override suspend fun deleteConnectorArchive(id: String) = api().deleteConnectorArchive(id)
        override suspend fun mailAccounts() = api().mailAccounts()
        override suspend fun addMailAccount(req: AddMailAccountRequest) = api().addMailAccount(req)
        override suspend fun deleteMailAccount(id: String) = api().deleteMailAccount(id)
        override suspend fun testMailAccount(id: String) = api().testMailAccount(id)
        override suspend fun googleSignInStart(loginHint: String?) = api().googleSignInStart(loginHint)
        override suspend fun googleSignInFinish(finish: GoogleFinish) = api().googleSignInFinish(finish)
        override suspend fun googleConfig() = api().googleConfig()
        override suspend fun larkStatus() = api().larkStatus()
        override suspend fun larkAccounts() = api().larkAccounts()
        override suspend fun larkChatChoices() = api().larkChatChoices()
        override suspend fun connectorLogin(id: String, step: String, values: Map<String, String>) = api().connectorLogin(id, step, values)
        override suspend fun connectorLogout(id: String) = api().connectorLogout(id)
        override suspend fun connectorThread(id: String, thread: String, before: String?, limit: Int) = api().connectorThread(id, thread, before, limit)
        override suspend fun connectorThreadReply(id: String, thread: String, action: String, text: String, replyTo: String?) =
            api().connectorThreadReply(id, thread, action, text, replyTo)
        override suspend fun microsoftConfig() = api().microsoftConfig()
        override suspend fun setMicrosoftClientId(clientId: String) = api().setMicrosoftClientId(clientId)
        override suspend fun microsoftSignInStart() = api().microsoftSignInStart()
        override suspend fun microsoftSignInFinish(finish: MicrosoftFinish) = api().microsoftSignInFinish(finish)
        override suspend fun larkMessageDisplay() = api().larkMessageDisplay()
        override suspend fun setLarkMessageDisplay(current: LarkMessageDisplay, key: String, value: Boolean) = api().setLarkMessageDisplay(current, key, value)
        override suspend fun messagesStatus() = api().messagesStatus()
        override suspend fun setMessagesAi(on: Boolean) = api().setMessagesAi(on)
        override suspend fun deleteMessagesData() = api().deleteMessagesData()
        override suspend fun messagesChats(before: String?) = api().messagesChats(before)
        override suspend fun messagesThread(chatId: String, before: String?) = api().messagesThread(chatId, before)
    }
}

/**
 * The hub's extension operation pushes, and null to list them again: after each hello (what was missed is not replayed)
 * and each managers push. A manager keeps its operations only in memory, and one that restarts (its own update, a crash)
 * while the socket stays up loses them with only a managers push to tell (#1832).
 */
internal fun extensionOperationUpdates(events: Flow<ServerEvent>): Flow<ExtensionOperation?> = events.transform { event ->
    if (event is Hello || event is ManagersEvent) emit(null)
    else if (event is ExtensionOperationEvent) emit(event.operation.copy(manager = pushManager(event.manager)))
}
