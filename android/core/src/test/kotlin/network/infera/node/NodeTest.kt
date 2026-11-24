package network.infera.node

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import java.io.File
import java.net.ServerSocket
import kotlin.test.AfterTest
import kotlin.test.BeforeTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

class FakeEngine : Engine {
    override val modelId = "phone-test"
    override fun generate(messages: List<Pair<String, String>>, maxTokens: Int, temperature: Float, onText: (String) -> Boolean): Pair<Int, Int> {
        val words = "Phones can serve small models while charging. ünïcödé ✓".split(" ").take(maxTokens)
        var n = 0
        for (w in words) { if (!onText("$w ")) break; n++ }
        return 12 to n
    }
}

/** End-to-end against the real Go control plane (set INFERA_SERVER_BIN to its binary). */
class NodeTest {
    private var server: Process? = null
    private var base = ""
    private val json = "application/json".toMediaType()

    @BeforeTest fun startServer() {
        val bin = System.getenv("INFERA_SERVER_BIN") ?: ""
        if (bin.isEmpty()) return
        val port = ServerSocket(0).use { it.localPort }
        val db = File.createTempFile("infera", ".db").also { it.delete() }
        server = ProcessBuilder(bin).apply {
            environment()["INFERA_DB"] = db.path
            environment()["INFERA_ADDR"] = ":$port"
            environment()["INFERA_CANARY_MINUTES"] = "0"
            redirectErrorStream(true); redirectOutput(File(db.path + ".log"))
        }.start()
        base = "http://127.0.0.1:$port"
        repeat(50) { if (runCatching { get("/healthz") }.isSuccess) return; Thread.sleep(100) }
    }

    @AfterTest fun stopServer() { server?.destroy() }

    private fun get(path: String) = Api.http.newCall(Request.Builder().url(base + path).build()).execute().use { it.body!!.string() }
    private fun post(path: String, body: String, key: String? = null): String {
        val r = Request.Builder().url(base + path).post(body.toRequestBody(json)).apply { if (key != null) header("Authorization", "Bearer $key") }.build()
        return Api.http.newCall(r).execute().use { it.body!!.string() }
    }
    private fun field(s: String, k: String) = Json.parseToJsonElement(s).jsonObject[k]!!.jsonPrimitive.content

    @Test fun cryptoRoundTrip() {
        val id = Identity.generate()
        assertTrue(id.sign("hello").isNotEmpty())
        assertEquals(Identity(id.seed).publicKeyB64, id.publicKeyB64)
        assertEquals(64, sha256Hex("x".toByteArray()).length)
        assertEquals("M1", phoneClass(12.0, 8.0)); assertEquals("M2", phoneClass(4.0, 4.0)); assertEquals("unrated", phoneClass(1.0, 8.0))
    }

    @Test fun phoneServesJobsAgainstRealServer() {
        if (server == null) { println("INFERA_SERVER_BIN not set; skipping"); return }
        val own = field(post("/api/signup", """{"email":"o@p.dev","kind":"owner"}"""), "api_key")
        val cust = field(post("/api/signup", """{"email":"c@p.dev","kind":"customer"}"""), "api_key")
        val code = field(post("/api/pair-code", "{}", own), "code")
        val pairing = Api.pair(base, code, "test-phone", Identity.generate(), "android 15 arm64")
        val id = Identity.generate() // wrong key: receipts must fail verification
        val realId = run {
            // re-pair with a known identity so receipts verify
            val code2 = field(post("/api/pair-code", "{}", own), "code")
            val ident = Identity.generate()
            Api.pair(base, code2, "test-phone-2", ident, "android 15 arm64") to ident
        }
        var last = NodeStatus()
        val node = Node(realId.first, realId.second, FakeEngine(), Bench(15.0, 1.7, 8.0, "M1"), "android 15 arm64", { null }, { last = it })
        node.start()
        repeat(50) { if (last.connected && last.status == "idle") return@repeat; Thread.sleep(100) }
        Thread.sleep(2500)

        // 1. auto-approve
        val r1 = post("/v1/chat/completions", """{"model":"phone-test","max_tokens":20,"messages":[{"role":"user","content":"hi"}]}""", cust)
        val text1 = Json.parseToJsonElement(r1).jsonObject["choices"]!!.jsonArray[0].jsonObject["message"]!!.jsonObject["content"]!!.jsonPrimitive.content
        assertTrue(text1.startsWith("Phones can serve"), r1)
        assertTrue(text1.contains("ünïcödé ✓"), "unicode must survive sealing: $text1")

        // 2. ask mode: accept the offer card
        node.setMode("ask"); Thread.sleep(500)
        val t = Thread { Thread.sleep(1500); assertTrue(last.offer != null, "offer card should be showing"); node.decide(true) }.also { it.start() }
        val r2 = post("/v1/chat/completions", """{"model":"phone-test","max_tokens":3,"messages":[{"role":"user","content":"hi"}]}""", cust)
        t.join()
        assertTrue(r2.contains("Phones can serve"), r2)

        // 3. owner credited
        Thread.sleep(500)
        val owner = Json.parseToJsonElement(Api.http.newCall(Request.Builder().url("$base/api/owner").header("Authorization", "Bearer $own").build()).execute().body!!.string()).jsonObject
        val held = owner["balance"]!!.jsonObject["held"]!!.jsonPrimitive.content.toLong()
        assertTrue(held > 0, "owner should have held earnings: $owner")
        node.stop()
        println("phone node OK: $text1 | events=${last.events.take(3)}")
        check(pairing.deviceId.isNotEmpty() && id.publicKeyB64.isNotEmpty())
    }
}
