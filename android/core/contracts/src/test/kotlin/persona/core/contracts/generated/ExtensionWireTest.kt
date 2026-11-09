package persona.core.contracts.generated

import persona.core.contracts.JsonCodec
import persona.core.contracts.zhAny

import java.io.File
import kotlinx.serialization.KSerializer
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.doubleOrNull
import org.junit.Assert.*
import org.junit.Test

// schema/extension (#1849): the generated types read the schema's examples and write them back the same; a component
// this app does not know, or cannot read, is kept as Unknown and the rest of the view still decodes.
class ExtensionWireTest {
    private val json = Json { ignoreUnknownKeys = true; encodeDefaults = true; explicitNulls = false }
    private val examples = File("../../../schema/extension/examples")

    private val types: Map<String, KSerializer<*>> = mapOf(
        "View" to ExtensionView.serializer(),
        "ActionResult" to ExtensionActionResult.serializer(),
        "UIParams" to ExtensionUIParams.serializer(),
        "UIImage" to ExtensionUIImage.serializer(),
        "UIImageParams" to ExtensionUIImageParams.serializer(),
        "UIPlaces" to ExtensionUIPlaces.serializer(),
        "InitializeParams" to ExtensionInitializeParams.serializer(),
        "InitializeResult" to ExtensionInitializeResult.serializer(),
        "Manifest" to ExtensionManifest.serializer(),
        "StoragePermission" to ExtensionStoragePermission.serializer(),
        "PersonaCard" to ExtensionPersonaCard.serializer(),
        "WidgetPlaces" to ExtensionWidgetPlaces.serializer(),
        "WidgetAnswer" to ExtensionWidgetAnswer.serializer(),
        "DeviceWidgets" to ExtensionDeviceWidgets.serializer(),
        "ShortcutPlaces" to ExtensionShortcutPlaces.serializer(),
        "ShortcutResult" to ExtensionShortcutResult.serializer(),
        "ExtensionNotification" to ExtensionNotification.serializer(),
        "AssistantView" to ExtensionAssistantView.serializer(),
        "ViewAction" to ExtensionViewAction.serializer(),
        // The card action requests and the 409 a changed card answers (#2604).
        "ViewActionInput" to ExtensionViewActionInput.serializer(),
        "AssistantHTMLActionInput" to ExtensionAssistantHTMLActionInput.serializer(),
        "ViewChangedError" to ExtensionViewChangedError.serializer(),
        // The messaging extension point's examples (#2242).
        "MessagingChanged" to ExtensionMessagingChanged.serializer(),
        "MessagingComposeAnswer" to ExtensionMessagingComposeAnswer.serializer(),
        "MessagingConversationPage" to ExtensionMessagingConversationPage.serializer(),
        "MessagingConversationRef" to ExtensionMessagingConversationRef.serializer(),
        "MessagingDocument" to ExtensionMessagingDocument.serializer(),
        "MessagingError" to ExtensionMessagingError.serializer(),
        "MessagingItem" to ExtensionMessagingItem.serializer(),
        "MessagingMessagePage" to ExtensionMessagingMessagePage.serializer(),
        "MessagingOutgoing" to ExtensionMessagingOutgoing.serializer(),
        // An extension's sign-in (#4507).
        "AuthStatus" to ExtensionAuthStatus.serializer(),
        "AuthPrompt" to ExtensionAuthPrompt.serializer(),
        // Contract-only native UI streaming (#5956); no renderer or runtime activation.
        "UIContext" to ExtensionUIContext.serializer(),
        "UIStreamSnapshot" to ExtensionUIStreamSnapshot.serializer(),
        "UIStreamDelta" to ExtensionUIStreamDelta.serializer(),
        "UIStreamEvent" to ExtensionUIStreamEvent.serializer(),
        "UIStreamRequest" to ExtensionUIStreamRequest.serializer(),
        "UIStreamClientEnvelope" to ExtensionUIStreamClientEnvelope.serializer(),
        "UIStreamServerEnvelope" to ExtensionUIStreamServerEnvelope.serializer(),
        "UIPageSnapshot" to ExtensionUIPageSnapshot.serializer(),
    )

    @Suppress("UNCHECKED_CAST")
    @Test fun examplesRoundTrip() {
        val files = examples.listFiles { f -> f.isFile && f.name.endsWith(".json") }.orEmpty()
        assertTrue("no examples in ${examples.absolutePath}", files.size > 10)
        for (f in files) {
            val serializer = types[f.name.substringBefore(".")] as KSerializer<Any?>? ?: fail("no type for ${f.name}") as Nothing
            val raw = json.parseToJsonElement(f.readText())
            val back = json.encodeToJsonElement(serializer, json.decodeFromJsonElement(serializer, raw))
            assertTrue("${f.name} round trip differs:\n$raw\n$back", same(raw, back))
        }
    }

    @Test fun storagePermissionsRoundTripWithProductionCodec() {
        val serializer = ExtensionStoragePermission.serializer()
        val declared = json.parseToJsonElement(File(examples, "StoragePermission.declared.json").readText())
        val sparse = listOf("{}", """{"keys":["preferences"]}""",
            """{"patterns":["state/*"],"queues":false,"blobs":false}""")
            .map(JsonCodec::parseToJsonElement)
        for (raw in sparse + declared) {
            val value = JsonCodec.decodeFromJsonElement(serializer, raw)
            val back = JsonCodec.encodeToJsonElement(serializer, value)
            assertTrue("storage permission production round trip differs: $raw / $back", same(raw, back))
        }
        val value = JsonCodec.decodeFromJsonElement(serializer, declared)
        assertEquals(listOf("preferences"), value.keys)
        assertEquals(listOf("state/*"), value.patterns)
        assertEquals(true, value.queues)
        assertEquals(true, value.blobs)

        for (raw in listOf("{}", """{"hub_api":true,"network":["https://example.com"]}""")) {
            val permissions = JsonCodec.decodeFromJsonElement(ExtensionPermissions.serializer(), JsonCodec.parseToJsonElement(raw))
            assertNull("other grants must not manufacture storage", permissions.storage)
            val back = JsonCodec.encodeToJsonElement(ExtensionPermissions.serializer(), permissions) as JsonObject
            assertFalse("absent storage must stay absent", "storage" in back)
        }
    }

    @Suppress("UNCHECKED_CAST")
    @Test fun streamExamplesRoundTripWithProductionCodec() {
        val files = examples.listFiles { f -> f.isFile && f.name.startsWith("UIStream") && f.name.endsWith(".json") }.orEmpty()
        assertTrue("no stream examples", files.size > 15)
        for (f in files) {
            val serializer = types.getValue(f.name.substringBefore(".")) as KSerializer<Any?>
            val raw = JsonCodec.parseToJsonElement(f.readText())
            val back = JsonCodec.encodeToJsonElement(serializer, JsonCodec.decodeFromJsonElement(serializer, raw))
            assertTrue("${f.name} production round trip differs:\n$raw\n$back", same(raw, back))
        }
    }

    @Test fun assistantExamplesRoundTrip() {
        val files = File(examples, "assistant").listFiles { f -> f.name.endsWith(".json") }.orEmpty()
        assertTrue("assistant examples missing", files.size >= 7)
        for (f in files) {
            val raw = json.parseToJsonElement(f.readText())
            val view = json.decodeFromJsonElement(ExtensionView.serializer(), raw)
            assertFalse("${f.name} has no fallback", view.fallback.isNullOrBlank())
            val back = json.encodeToJsonElement(ExtensionView.serializer(), view)
            assertTrue("${f.name} round trip differs:\n$raw\n$back", same(raw, back))
        }
    }

    @Test fun unknownComponentsAreKept() {
        val view = json.decodeFromString(ExtensionView.serializer(), """{"sections":[{"items":[
            {"type":"hologram","depth":3},
            {"type":"metric","label":"今天","value":"1.2M","tone":"sparkly"},
            {"type":"chart","kind":"bar","series":"not a list"}]}]}""")
        val items = view.sections.single().items
        assertEquals(ExtensionComponent.Unknown("hologram", items[0].let { (it as ExtensionComponent.Unknown).raw }), items[0])
        assertEquals("sparkly", ((items[1] as ExtensionMetricComponent).tone).zhAny())
        assertTrue(items[2] is ExtensionComponent.Unknown)
        assertEquals("chart", (items[2].type).zhAny())
        // An unknown component writes back what it was.
        val out = json.encodeToJsonElement(ExtensionView.serializer(), view) as JsonObject
        assertTrue(out.toString().contains("hologram"))
    }

    /** JSON equality with numbers compared as numbers (8432 and 8432.0 are the same). */
    private fun same(a: JsonElement, b: JsonElement): Boolean = when {
        a is JsonObject && b is JsonObject -> a.keys == b.keys && a.keys.all { same(a[it]!!, b[it]!!) }
        a is JsonArray && b is JsonArray -> a.size == b.size && a.indices.all { same(a[it], b[it]) }
        a is JsonNull || b is JsonNull -> a == b
        a is JsonPrimitive && b is JsonPrimitive ->
            if (!a.isString && !b.isString && a.doubleOrNull != null) a.doubleOrNull == b.doubleOrNull else a == b
        else -> false
    }
}
