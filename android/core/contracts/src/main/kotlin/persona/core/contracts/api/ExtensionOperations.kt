package persona.core.contracts.api

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * Installing, upgrading or removing an extension in steps (docs/protocol/protocol.md "Extension install, upgrade and uninstall progress", #3277): the hub
 * runs each as an operation and pushes `extension_operation` as it goes, to a client that says `extension_operations`.
 * Every field but the id has a default and unknown steps or states are kept as they are: a newer hub may add some.
 */
@Serializable
data class ExtensionOperation(
    val id: String = "",
    /** `install`, `upgrade` or `uninstall`. */
    val kind: String = "",
    val extension: String = "",
    val name: String? = null,
    val market: String? = null,
    val source: String? = null,
    val version: String? = null,
    val from: String? = null,
    val actor: String? = null,
    /** `running`, `needs_consent`, `succeeded`, `failed` or `canceled`. */
    val state: String = "",
    /** The step it is at, or stopped at. */
    val step: String? = null,
    val steps: List<ExtensionOpStep> = emptyList(),
    val error: String? = null,
    @SerialName("error_code") val errorCode: String? = null,
    @SerialName("preview_id") val previewId: String? = null,
    val retryable: Boolean = false,
    @SerialName("started_at") val startedAt: String = "",
    @SerialName("updated_at") val updatedAt: String = "",
    @SerialName("ended_at") val endedAt: String? = null,
    /** Only from GET of one that waits for consent: what the user agrees to. */
    val preview: ExtensionPreview? = null,
    /** What the user should do once it succeeded: `auth` (#4507) when the extension signs in and is not signed in yet. */
    val next: String? = null,
    /** The manager whose it is, from its push (pushManager); null in a list, which is the default manager's. */
    val manager: String? = null,
) {
    val active: Boolean get() = state == Running || state == NeedsConsent
    val failed: Boolean get() = state == Failed

    companion object {
        const val Running = "running"
        const val NeedsConsent = "needs_consent"
        const val Succeeded = "succeeded"
        const val Failed = "failed"
        const val Canceled = "canceled"
    }
}

/** One step of an operation: its state, a download's bytes, the computers it reached. */
@Serializable
data class ExtensionOpStep(
    val id: String = "",
    /** `pending`, `running`, `done`, `failed` or `skipped`. */
    val state: String = "",
    val bytes: Long? = null,
    val total: Long? = null,
    /** The package came from the hub's cache: no download. */
    val cached: Boolean = false,
    val machines: ExtensionOpMachines? = null,
    /** The `tools` step (#4507): its command-line programs in the order they install; [bytes] / [total] are the one downloading. */
    val tools: List<ExtensionOpTool>? = null,
    @SerialName("started_at") val startedAt: String? = null,
    @SerialName("ended_at") val endedAt: String? = null,
)

/** The `nodes` step: how many of the computers that run it have it, and why the others do not. */
@Serializable
data class ExtensionOpMachines(
    val done: Int = 0,
    val total: Int = 0,
    val errors: Map<String, String>? = null,
)

/** One command-line program of the `tools` step: `pending`, `running`, `done` or `failed`; [reused] is the machine's own copy. */
@Serializable
data class ExtensionOpTool(
    val name: String = "",
    val state: String = "",
    val reused: Boolean = false,
)
