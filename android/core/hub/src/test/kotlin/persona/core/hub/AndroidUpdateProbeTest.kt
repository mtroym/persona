package persona.core.hub

import java.io.IOException
import java.net.*
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.runBlocking
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.Assert.*
import org.junit.Test

class AndroidUpdateProbeTest {
    @Test fun failureRetainsSocketLocationWithoutMessagesOrSourceNames() {
        val timeout = SocketTimeoutException("https://secret.example/?token=secret")
        timeout.stackTrace = arrayOf(StackTraceElement("java.net.Socket", "connect", "private-token.kt", 751))
        val report = updateProbeFailure("metadata", 0, 10000, HubFailure.from(timeout))
        assertTrue(report.contains("stage=metadata bytes=0 elapsedMs=10000 kind=Timeout status=null"))
        assertTrue(report.contains("SocketTimeoutException[java.net.Socket.connect:751]"))
        assertFalse(report.contains("secret"))
        assertFalse(report.contains("private-token"))
    }

    @Test fun rawDownloadTimeoutKeepsProgressAndErrorKind() {
        val report = updateProbeFailure("download", 8192, 15000, SocketTimeoutException("private response"))
        assertTrue(report.contains("stage=download bytes=8192 elapsedMs=15000 kind=Timeout status=null"))
        assertFalse(report.contains("private response"))
    }

    @Test fun productionMetadataBypassesConfiguredHttpProxy() = runBlocking {
        val previous = ProxySelector.getDefault()
        val selections = java.util.concurrent.CopyOnWriteArrayList<String>()
        ProxySelector.setDefault(object : ProxySelector() {
            override fun select(uri: URI): List<Proxy> {
                selections.add(uri.scheme)
                return listOf(Proxy(Proxy.Type.HTTP, InetSocketAddress("127.0.0.1", 1)))
            }
            override fun connectFailed(uri: URI, address: SocketAddress, error: IOException) {
                throw IllegalStateException("Unexpected proxy failure")
            }
        })
        try {
            MockWebServer().use { server ->
                server.enqueue(MockResponse().setBody("""{"version_code":104564,"version_name":"offline","sha256":"${"a".repeat(64)}"}"""))
                server.start(InetAddress.getLoopbackAddress(), 0)
                HubPairingClient(null, null, listOf(server.url("/").toString()), "offline").use { client ->
                    assertEquals(Proxy.NO_PROXY, client.http.client.proxy)
                    assertEquals(10000, client.http.client.connectTimeoutMillis)
                    assertEquals(15000, client.http.client.readTimeoutMillis)
                    assertNotNull(client.checkUpdate("local-test-token"))
                    assertEquals("/api/v1/app/android/latest", server.takeRequest().path)
                    assertTrue(selections.all { it == "socket" })
                    println("OFFLINE: metadata received directly; configured HTTP proxy at closed loopback port ignored; selector schemes=$selections; connect=10000ms read=15000ms")
                }
            }
        } finally { ProxySelector.setDefault(previous) }
    }

    @Test fun metadata503KeepsStatusAndIsDistinctFromTimeout() = runBlocking {
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setResponseCode(503).setBody("service unavailable"))
            server.start(InetAddress.getLoopbackAddress(), 0)
            HubPairingClient(null, null, listOf(server.url("/").toString()), "offline").use { client ->
                try { client.checkUpdate("local-test-token"); fail("Expected 503") }
                catch (failure: HubFailure) {
                    assertEquals(HubError.Server, failure.kind)
                    assertEquals(503, failure.status)
                    println("OFFLINE: metadata HTTP503 => kind=Server status=503, not Timeout")
                }
            }
        }
    }

    @Test fun metadataBodyStallUsesReadTimeout() = runBlocking {
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setBody("{}").setBodyDelay(16, TimeUnit.SECONDS))
            server.start(InetAddress.getLoopbackAddress(), 0)
            HubPairingClient(null, null, listOf(server.url("/").toString()), "offline").use { client ->
                val start = System.nanoTime()
                try { client.checkUpdate("local-test-token"); fail("Expected timeout") }
                catch (failure: HubFailure) {
                    assertEquals(HubError.Timeout, failure.kind)
                    assertNull(failure.status)
                    assertTrue(failure.cause is SocketTimeoutException)
                    println("OFFLINE: metadata body stall => Timeout, elapsedMs=${(System.nanoTime()-start)/1_000_000}; cause=${failure.cause!!::class.java.simpleName}")
                }
            }
        }
    }
}
