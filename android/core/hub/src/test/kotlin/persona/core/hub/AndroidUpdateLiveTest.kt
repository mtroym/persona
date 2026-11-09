package persona.core.hub

import kotlinx.coroutines.runBlocking
import okhttp3.HttpUrl.Companion.toHttpUrl
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Test
import java.io.OutputStream
import java.security.DigestOutputStream
import java.security.MessageDigest

/** Optional read-only probe of the same TLS update client used by the Android app. */
class AndroidUpdateLiveTest {
    @Test fun checksAndDownloadsPublishedApkThroughProductionClient() = runBlocking {
        val url = System.getenv("PERSONA_ANDROID_UPDATE_HUB")
        val token = System.getenv("PERSONA_ANDROID_UPDATE_TOKEN")
        val version = System.getenv("PERSONA_ANDROID_UPDATE_VERSION")
        val checksum = System.getenv("PERSONA_ANDROID_UPDATE_SHA256")
        assumeTrue("Live update endpoint, credential, version and hash were not supplied",
            listOf(url, token, version, checksum).all { !it.isNullOrBlank() })
        val expectedVersion = requireNotNull(version!!.toLongOrNull()) { "Invalid expected version" }
        require(checksum!!.matches(Regex("[a-fA-F0-9]{64}"))) { "Invalid expected SHA-256" }
        val base = url!!.toHttpUrl()
        require(base.isHttps && base.username.isEmpty() && base.password.isEmpty() && base.query == null && base.fragment == null) {
            "Live update probe requires a credential-free HTTPS origin"
        }
        var stage = "initialization"
        var bytes = 0L
        var stageStarted = System.nanoTime()
        try {
            HubPairingClient(
                System.getenv("PERSONA_ANDROID_UPDATE_HUB_ID")?.takeIf { it.isNotBlank() },
                System.getenv("PERSONA_ANDROID_UPDATE_FINGERPRINT")?.takeIf { it.isNotBlank() },
                listOf(base.toString()), "android-update-live-probe",
            ).use { client ->
                val http = client.http.client
                assertEquals("Production update transport must remain direct", java.net.Proxy.NO_PROXY, http.proxy)
                println("Android update live probe transport=DIRECT connectTimeoutMs=${http.connectTimeoutMillis} " +
                    "readTimeoutMs=${http.readTimeoutMillis} " +
                    "artifactProxyEnvironmentIgnored=${!System.getenv("PERSONA_ANDROID_UPDATE_PROXY").isNullOrBlank()}")
                stage = "metadata"
                stageStarted = System.nanoTime()
                val update = requireNotNull(client.checkUpdate(token!!)) { "No published Android update" }
                assertEquals("Published version differs", expectedVersion, update.versionCode)
                assertEquals("Metadata hash differs", checksum.lowercase(), update.sha256?.lowercase())
                val digest = MessageDigest.getInstance("SHA-256")
                val counter = object : OutputStream() {
                    override fun write(value: Int) { bytes++ }
                    override fun write(buffer: ByteArray, offset: Int, length: Int) { bytes += length }
                }
                stage = "download"
                stageStarted = System.nanoTime()
                DigestOutputStream(counter, digest).use { client.download(token, update, it) }
                assertTrue("APK response is empty", bytes > 0)
                val actual = digest.digest().joinToString("") { "%02x".format(it) }
                assertEquals("Downloaded hash differs", checksum.lowercase(), actual)
                println("Android update live probe PASS: version=$expectedVersion sha256=$actual bytes=$bytes")
            }
        } catch (error: Exception) {
            val elapsedMs = (System.nanoTime() - stageStarted) / 1_000_000
            throw AssertionError(updateProbeFailure(stage, bytes, elapsedMs, error))
        }
    }
}

/** Retain failure locations, never exception messages, URLs, remote bodies or the original cause. */
internal fun updateProbeFailure(stage: String, bytes: Long, elapsedMs: Long, error: Exception): String {
    val causes = generateSequence(error as Throwable) { it.cause }.take(8).joinToString(" -> ") { cause ->
        val frames = cause.stackTrace.take(6).joinToString(",") { "${it.className}.${it.methodName}:${it.lineNumber}" }
        "${cause.javaClass.simpleName}[$frames]"
    }
    val failure = HubFailure.from(error)
    return "Live update probe failed: stage=$stage bytes=$bytes elapsedMs=$elapsedMs " +
        "kind=${failure.kind} status=${failure.status} causes=$causes"
}
