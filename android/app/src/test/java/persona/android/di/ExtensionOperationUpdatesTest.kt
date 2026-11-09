package persona.android.di

import persona.core.contracts.ExtensionOperationEvent
import persona.core.contracts.Hello
import persona.core.contracts.ManagersEvent
import persona.core.contracts.ServerEvent
import persona.core.contracts.SessionsEvent
import persona.core.contracts.api.ExtensionOperation
import kotlinx.coroutines.flow.asFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.flow.toList
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Test

class ExtensionOperationUpdatesTest {
    @Test fun aManagersPushListsOperationsAgainLikeAHello() = runBlocking {
        // #1832: a manager that restarts while the socket stays up loses its operations, and only a managers push says so.
        val op = ExtensionOperation("op1", "install", "example-notes", state = "running")
        val events = flowOf(Hello(), ExtensionOperationEvent(op), SessionsEvent("mac", emptyList()), ManagersEvent)
        assertEquals(listOf(null, op.copy(manager = "assistant"), null), extensionOperationUpdates(events).toList())
    }

    @Test fun anOperationSaysWhoseItIs() = runBlocking {
        // #1834: the hub names every manager but the assistant it starts; the catalog page follows only the default's.
        val pushed = listOfNotNull(
            ServerEvent.decode("""{"type":"extension_operation","manager":"m2","operation":{"id":"op1","extension":"example-notes"}}"""),
            ServerEvent.decode("""{"type":"extension_operation","operation":{"id":"op2","extension":"example-notes"}}"""))
        assertEquals(listOf("m2", "assistant"), extensionOperationUpdates(pushed.asFlow()).toList().map { it?.manager })
    }
}
