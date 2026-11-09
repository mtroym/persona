@file:OptIn(kotlinx.serialization.ExperimentalSerializationApi::class)

package persona.core.contracts

import persona.core.contracts.stream.NativeStreamSchema
import persona.core.contracts.generated.ExtensionUIStreamServerEnvelope
import kotlinx.serialization.KSerializer
import kotlinx.serialization.SerialName
import kotlinx.serialization.json.JsonNames
import kotlinx.serialization.Serializable
import kotlinx.serialization.KeepGeneratedSerializer
import kotlinx.serialization.SerializationException
import kotlinx.serialization.descriptors.buildClassSerialDescriptor
import kotlinx.serialization.encoding.Decoder
import kotlinx.serialization.encoding.Encoder
import kotlinx.serialization.json.*

@Serializable(with = ServerEventSerializer::class)
sealed interface ServerEvent {
    companion object {
        /** Unknown events and invalid required payloads do not enter the event stream. */
        fun decode(text: String): ServerEvent? = runCatching {
            JsonCodec.decodeFromString<ServerEvent>(text).takeUnless { it === UnknownEvent }
        }.getOrNull()

        /** The same as [decode] for a frame already parsed, so a socket frame is read from text once. */
        fun decode(element: JsonElement): ServerEvent? = runCatching {
            JsonCodec.decodeFromJsonElement<ServerEvent>(element).takeUnless { it === UnknownEvent }
        }.getOrNull()
    }
}

/** Private native frames retain raw presence; generated envelopes are their typed dispatch projection. */
data class ExtensionUiStreamEvent(val raw: JsonObject) : ServerEvent {
    val envelope: ExtensionUIStreamServerEnvelope = JsonCodec.decodeFromJsonElement(ExtensionUIStreamServerEnvelope.serializer(), raw)
}

@KeepGeneratedSerializer
@Serializable(with = HelloSerializer::class)
data class Hello(
    @SerialName("server_version") val serverVersion: String? = null,
    @SerialName("client_id") val clientId: String? = null,
    val features: List<String> = emptyList(),
    @SerialName("hub_id") val hubId: String? = null,
    val endpoints: List<Endpoint> = emptyList(),
    @SerialName("tls_port") val tlsPort: Int? = null,
    val fp: String? = null,
    /** The hub's display name (docs/archive/operations/plan-hub-mesh.md 2.6). */
    @SerialName("hub_name") val hubName: String? = null,
    /** Every hub of this hub's mesh, itself included; only in the authenticated hello. */
    val mesh: HubMesh? = null,
    /** The hub's broadcast stream (#1574): its broadcasts carry `event_seq`, numbered within it. Absent on older hubs. */
    @SerialName("event_stream") val eventStream: String? = null,
    /** The stream's last broadcast number when this socket connected. */
    @SerialName("event_seq") val eventSeq: Long? = null,
    /** The hub replays every broadcast this client missed right after the hello: no refetch is needed. */
    val resumed: Boolean? = null,
    val channel: String? = null,
    val build: Long? = null,
    val protocol: Int? = null,
    @SerialName("min_app_build") val minAppBuild: Int? = null,
) : ServerEvent

@KeepGeneratedSerializer
@Serializable(with = HubMeshSerializer::class)
data class HubMesh(val hubs: List<MeshHub> = emptyList())

/** One hub of a mesh as the hub the client is connected to reports it; trusted because the hello is authenticated. */
@KeepGeneratedSerializer
@Serializable(with = MeshHubSerializer::class)
data class MeshHub(
    @SerialName("hub_id") val hubId: String = "",
    val name: String = "",
    val online: Boolean = false,
    val self: Boolean = false,
    val endpoints: List<String> = emptyList(),
    @SerialName("tls_port") val tlsPort: Int? = null,
    val fp: String? = null,
    val center: Boolean? = null,
)

@KeepGeneratedSerializer
@Serializable(with = ItemsEventSerializer::class)
data class ItemsEvent(
    @SerialName("machine_id") val machineId: String,
    @SerialName("session_id") val sessionId: String,
    val items: List<Item> = emptyList(), val history: String? = null,
    @SerialName("from_seq") val fromSeq: Int? = null,
    @SerialName("to_seq") val toSeq: Int? = null,
    val backlog: Boolean = false,
    @SerialName("has_more") val hasMore: Boolean? = null,
    @SerialName("more_after") val moreAfter: Boolean = false,
) : ServerEvent

@KeepGeneratedSerializer
@Serializable(with = SessionsEventSerializer::class)
data class SessionsEvent(
    @SerialName("machine_id") val machineId: String, val sessions: List<Session> = emptyList(),
) : ServerEvent

/** A bounded runtime list invalidation hint, never a completed HTTP baseline. */
@Serializable
data class SessionsListEvent(val revision: String) : ServerEvent

@KeepGeneratedSerializer
@Serializable(with = MachinesEventSerializer::class)
data class MachinesEvent(
    val machines: List<Machine> = emptyList(),
) : ServerEvent

@KeepGeneratedSerializer
@Serializable(with = RevsEventSerializer::class)
data class RevsEvent(
    @SerialName("machine_id") val machineId: String, val revs: Map<String, Long> = emptyMap(),
) : ServerEvent

@KeepGeneratedSerializer
@Serializable(with = FleetEventSerializer::class)
data class FleetEvent(
    val fleet: FleetState,
) : ServerEvent

@KeepGeneratedSerializer
@Serializable(with = InteractionEventSerializer::class)
data class InteractionEvent(
    val interaction: Interaction,
) : ServerEvent

@KeepGeneratedSerializer
@Serializable(with = InteractionResolvedEventSerializer::class)
data class InteractionResolvedEvent(
    val id: String,
) : ServerEvent

@KeepGeneratedSerializer
@Serializable(with = EndpointsEventSerializer::class)
data class EndpointsEvent(
    val endpoints: List<Endpoint> = emptyList(),
) : ServerEvent

@KeepGeneratedSerializer
@Serializable(with = ClientHelloAckSerializer::class)
data class ClientHelloAck(
    val heartbeat: Heartbeat,
) : ServerEvent

@KeepGeneratedSerializer
@Serializable(with = PongSerializer::class)
data class Pong(
    val id: String,
) : ServerEvent

@KeepGeneratedSerializer
@Serializable(with = NotifyEventSerializer::class)
data class NotifyEvent(
    @WireEnum("interaction", "done", "assistant", "alert", "unknown") val kind: String,
    @SerialName("machine_id") val machineId: String,
    @SerialName("session_id") val sessionId: String,
    val title: String = "", val subtitle: String = "", val body: String = "",
    @SerialName("interaction_id") val interactionId: String? = null,
    /** alert: the caller's id; the same one again replaces the earlier alert (#5614). */
    val id: String? = null,
) : ServerEvent

/**
 * An extension changed the badge of its entry on the navigation (`nav_page`, docs/extensions/extensions.md "UI slots" "Badges"): `null`
 * clears it. The event is `extension_ui`; a hub before #1844 says `plugin_ui`, and a newer one sends both (the same badge
 * twice is no change). The app changes the badge without reading the list again.
 */
@KeepGeneratedSerializer
@Serializable(with = ExtensionUiEventSerializer::class)
data class ExtensionUiEvent(
    @SerialName("extension") @JsonNames("plugin") val extension: String,
    /** The UI it is about; none (only with [invalidate]): all of the extension's. */
    val id: String? = null,
    val badge: persona.core.contracts.api.ExtensionViewBadge? = null,
    /**
     * UI version 3 (`ui.invalidate`, only as `extension_ui`): the extension's data changed, the views on screen are drawn
     * again. Not a badge change: the badge stays.
     */
    val invalidate: Boolean = false,
    /** The manager that sent it ([pushManager]): none from the assistant the hub starts (docs/assistant/managers.md). */
    val manager: String? = null,
) : ServerEvent

/** The hub's update status changed (docs/operations/hub-update.md "In the app"): `update` is what GET /api/v1/hub/update answers. */
@KeepGeneratedSerializer
@Serializable(with = HubUpdateEventSerializer::class)
data class HubUpdateEvent(val update: persona.core.contracts.api.HubUpdateStatus) : ServerEvent

/**
 * An extension's install, upgrade or uninstall moved on (docs/protocol/protocol.md "Extension install, upgrade and uninstall progress"): only to a client
 * that says `extension_operations`, and not replayed after a reconnect (the page lists them again).
 */
@KeepGeneratedSerializer
@Serializable(with = ExtensionOperationEventSerializer::class)
data class ExtensionOperationEvent(val operation: persona.core.contracts.api.ExtensionOperation, val manager: String? = null) : ServerEvent

/**
 * An extension's sign-in changed (#4507: signed in, out, expired, or the extension said so): only from a hub that says
 * `extension_auth`; the extension's page updates in place.
 */
@KeepGeneratedSerializer
@Serializable(with = ExtensionAuthEventSerializer::class)
data class ExtensionAuthEvent(val extension: String, val auth: persona.core.contracts.api.ExtensionAuth, val manager: String? = null) : ServerEvent

/**
 * Whose an extension push is (`extension_ui`, `extension_operation`, `extension_auth`): the hub puts a top-level
 * `manager` on every manager's but the assistant's it starts, so none is the assistant's (docs/assistant/managers.md,
 * the same extension id under several managers, #1834).
 */
fun pushManager(manager: String?): String = manager?.takeIf { it.isNotBlank() } ?: persona.core.contracts.api.ManagerRoster.ASSISTANT_MANAGER

/**
 * The managers' roster changed (docs/assistant/managers.md: made, renamed, connected, gone): only to an app that asks for the
 * `managers` feature, and not replayed after a reconnect (the page reads the list again).
 */
data object ManagersEvent : ServerEvent

data object UnknownEvent : ServerEvent

object ServerEventSerializer : KSerializer<ServerEvent> {
    override val descriptor = buildClassSerialDescriptor("persona.core.contracts.ServerEvent")
    override fun deserialize(decoder: Decoder): ServerEvent {
        val input = decoder as? JsonDecoder ?: throw SerializationException("Events require JSON")
        val obj = input.decodeJsonElement() as? JsonObject ?: throw SerializationException("Expected event object")
        val type = (obj["type"] as? JsonPrimitive)?.takeIf { it.isString }?.content
        return when (type) {
            "hello" -> input.json.decodeFromJsonElement<Hello>(obj)
            "items" -> input.json.decodeFromJsonElement<ItemsEvent>(obj)
            "sessions" -> input.json.decodeFromJsonElement<SessionsEvent>(obj).let { event ->
                event.copy(sessions = event.sessions.map { it.copy(machineId = event.machineId) })
            }
            "sessions_list" -> input.json.decodeFromJsonElement<SessionsListEvent>(obj).also {
                require(it.revision.isNotEmpty()) { "A list hint has a revision" }
            }
            "machines" -> input.json.decodeFromJsonElement<MachinesEvent>(obj)
            "revs" -> input.json.decodeFromJsonElement<RevsEvent>(obj)
            "fleet" -> input.json.decodeFromJsonElement<FleetEvent>(obj)
            "interaction" -> input.json.decodeFromJsonElement<InteractionEvent>(obj)
            "interaction_resolved" -> input.json.decodeFromJsonElement<InteractionResolvedEvent>(obj)
            "endpoints" -> input.json.decodeFromJsonElement<EndpointsEvent>(obj)
            "client_hello_ack" -> input.json.decodeFromJsonElement<ClientHelloAck>(obj)
            "pong" -> input.json.decodeFromJsonElement<Pong>(obj)
            "notify" -> input.json.decodeFromJsonElement<NotifyEvent>(obj)
            "extension_ui", "plugin_ui" -> input.json.decodeFromJsonElement<ExtensionUiEvent>(obj).also {
                require(it.invalidate || !it.id.isNullOrEmpty()) { "A badge names its UI" }
            }
            "extension_ui_stream" -> ExtensionUiStreamEvent(NativeStreamSchema.parse(obj.toString(), "UIStreamServerEnvelope"))
            "hub_update" -> input.json.decodeFromJsonElement<HubUpdateEvent>(obj)
            "managers" -> ManagersEvent
            "extension_operation" -> input.json.decodeFromJsonElement<ExtensionOperationEvent>(obj).also {
                require(it.operation.id.isNotEmpty()) { "An operation has its id" }
            }
            "extension_auth" -> input.json.decodeFromJsonElement<ExtensionAuthEvent>(obj).also {
                require(it.extension.isNotEmpty()) { "A sign-in names its extension" }
            }
            "lock_test" -> input.json.decodeFromJsonElement<LockTestEvent>(obj).also {
                require(it.testId.isNotBlank() && it.dueAt.isNotBlank() && it.sentAt.isNotBlank())
            }
            "delta" -> input.json.decodeFromJsonElement<DeltaEvent>(obj)
            "screen" -> input.json.decodeFromJsonElement<ScreenEvent>(obj)
            "tool_call" -> input.json.decodeFromJsonElement<ToolCallEvent>(obj)
            "hook_event" -> input.json.decodeFromJsonElement<HookEvent>(obj)
            "lark" -> input.json.decodeFromJsonElement<LarkEvent>(obj)
            "inbox" -> input.json.decodeFromJsonElement<InboxEvent>(obj)
            "todos" -> input.json.decodeFromJsonElement<TodosEvent>(obj)
            "assistant_feed" -> input.json.decodeFromJsonElement<AssistantFeedEvent>(obj)
            "task_plan" -> input.json.decodeFromJsonElement<TaskPlanEvent>(obj)
            "assistant_view" -> input.json.decodeFromJsonElement<AssistantViewEvent>(obj)
            "assistant_view_action" -> input.json.decodeFromJsonElement<AssistantViewActionEvent>(obj)
            "assistant_view_block" -> input.json.decodeFromJsonElement<AssistantViewBlockEvent>(obj).also {
                require(it.conversation.isNotEmpty() && it.key.isNotEmpty()) { "A block names its conversation and key" }
            }
            "personas" -> input.json.decodeFromJsonElement<PersonasEvent>(obj)
            "look" -> input.json.decodeFromJsonElement<LookEvent>(obj).let {
                it.copy(id = ProtocolCompatibility.canonicalBuiltin(it.id)!!, effective = ProtocolCompatibility.canonicalBuiltin(it.effective)!!)
            }
            "desktops" -> input.json.decodeFromJsonElement<DesktopsEvent>(obj)
            "terminal" -> input.json.decodeFromJsonElement<TerminalEvent>(obj).also { event ->
                if (!event.streamEnded && (event.epoch == null || event.seq == null || event.frameBytes == null)) {
                    throw SerializationException("Terminal frame requires epoch, seq and base64 data")
                }
            }
            "terminal_control" -> input.json.decodeFromJsonElement<TerminalControlEvent>(obj)
            "error" -> input.json.decodeFromJsonElement<ErrorEvent>(obj)
            else -> UnknownEvent
        }
    }

    override fun serialize(encoder: Encoder, value: ServerEvent) {
        val output = encoder as? JsonEncoder ?: throw SerializationException("Events require JSON")
        val (type, payload) = when (value) {
            is Hello -> "hello" to output.json.encodeToJsonElement(value)
            is ItemsEvent -> "items" to output.json.encodeToJsonElement(value)
            is SessionsEvent -> "sessions" to output.json.encodeToJsonElement(value)
            is SessionsListEvent -> "sessions_list" to output.json.encodeToJsonElement(value)
            is MachinesEvent -> "machines" to output.json.encodeToJsonElement(value)
            is RevsEvent -> "revs" to output.json.encodeToJsonElement(value)
            is FleetEvent -> "fleet" to output.json.encodeToJsonElement(value)
            is InteractionEvent -> "interaction" to output.json.encodeToJsonElement(value)
            is InteractionResolvedEvent -> "interaction_resolved" to output.json.encodeToJsonElement(value)
            is EndpointsEvent -> "endpoints" to output.json.encodeToJsonElement(value)
            is ClientHelloAck -> "client_hello_ack" to output.json.encodeToJsonElement(value)
            is Pong -> "pong" to output.json.encodeToJsonElement(value)
            is NotifyEvent -> "notify" to output.json.encodeToJsonElement(value)
            is ExtensionUiStreamEvent -> "extension_ui_stream" to value.raw
            is ExtensionUiEvent -> "extension_ui" to output.json.encodeToJsonElement(value)
            is HubUpdateEvent -> "hub_update" to output.json.encodeToJsonElement(value)
            ManagersEvent -> "managers" to JsonObject(emptyMap())
            is ExtensionOperationEvent -> "extension_operation" to output.json.encodeToJsonElement(value)
            is ExtensionAuthEvent -> "extension_auth" to output.json.encodeToJsonElement(value)
            is LockTestEvent -> "lock_test" to output.json.encodeToJsonElement(value)
            is DeltaEvent -> "delta" to output.json.encodeToJsonElement(value)
            is ScreenEvent -> "screen" to output.json.encodeToJsonElement(value)
            is ToolCallEvent -> "tool_call" to output.json.encodeToJsonElement(value)
            is HookEvent -> "hook_event" to output.json.encodeToJsonElement(value)
            is LarkEvent -> "lark" to output.json.encodeToJsonElement(value)
            is InboxEvent -> "inbox" to output.json.encodeToJsonElement(value)
            is TodosEvent -> "todos" to output.json.encodeToJsonElement(value)
            is AssistantFeedEvent -> "assistant_feed" to output.json.encodeToJsonElement(value)
            is TaskPlanEvent -> "task_plan" to output.json.encodeToJsonElement(value)
            is AssistantViewEvent -> "assistant_view" to output.json.encodeToJsonElement(value)
            is AssistantViewActionEvent -> "assistant_view_action" to output.json.encodeToJsonElement(value)
            is AssistantViewBlockEvent -> "assistant_view_block" to output.json.encodeToJsonElement(value)
            is PersonasEvent -> "personas" to output.json.encodeToJsonElement(value)
            is LookEvent -> "look" to output.json.encodeToJsonElement(value)
            is DesktopsEvent -> "desktops" to output.json.encodeToJsonElement(value)
            is TerminalEvent -> "terminal" to output.json.encodeToJsonElement(value)
            is TerminalControlEvent -> "terminal_control" to output.json.encodeToJsonElement(value)
            is ErrorEvent -> "error" to output.json.encodeToJsonElement(value)
            UnknownEvent -> throw SerializationException("Unknown events cannot be sent")
        }
        output.encodeJsonElement(JsonObject(payload.jsonObject + ("type" to JsonPrimitive(type))))
    }
}

object HelloSerializer : WireSerializer<Hello>(Hello.generatedSerializer())
object HubMeshSerializer : WireSerializer<HubMesh>(HubMesh.generatedSerializer())
object MeshHubSerializer : WireSerializer<MeshHub>(MeshHub.generatedSerializer())
object ItemsEventSerializer : WireSerializer<ItemsEvent>(ItemsEvent.generatedSerializer())
object SessionsEventSerializer : WireSerializer<SessionsEvent>(SessionsEvent.generatedSerializer())
object MachinesEventSerializer : WireSerializer<MachinesEvent>(MachinesEvent.generatedSerializer())
object RevsEventSerializer : WireSerializer<RevsEvent>(RevsEvent.generatedSerializer())
object FleetEventSerializer : WireSerializer<FleetEvent>(FleetEvent.generatedSerializer())
object InteractionEventSerializer : WireSerializer<InteractionEvent>(InteractionEvent.generatedSerializer())
object InteractionResolvedEventSerializer : WireSerializer<InteractionResolvedEvent>(InteractionResolvedEvent.generatedSerializer())
object EndpointsEventSerializer : WireSerializer<EndpointsEvent>(EndpointsEvent.generatedSerializer())
object ClientHelloAckSerializer : WireSerializer<ClientHelloAck>(ClientHelloAck.generatedSerializer())
object PongSerializer : WireSerializer<Pong>(Pong.generatedSerializer())
object NotifyEventSerializer : WireSerializer<NotifyEvent>(NotifyEvent.generatedSerializer())
object ExtensionUiEventSerializer : WireSerializer<ExtensionUiEvent>(ExtensionUiEvent.generatedSerializer())
object HubUpdateEventSerializer : WireSerializer<HubUpdateEvent>(HubUpdateEvent.generatedSerializer())
object ExtensionOperationEventSerializer : WireSerializer<ExtensionOperationEvent>(ExtensionOperationEvent.generatedSerializer())
object ExtensionAuthEventSerializer : WireSerializer<ExtensionAuthEvent>(ExtensionAuthEvent.generatedSerializer())
