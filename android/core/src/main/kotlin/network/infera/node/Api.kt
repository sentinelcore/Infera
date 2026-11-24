package network.infera.node

import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import java.util.concurrent.TimeUnit

data class Pairing(val server: String, val deviceId: String, val token: String)

object Api {
    val http: OkHttpClient = OkHttpClient.Builder()
        .connectTimeout(15, TimeUnit.SECONDS)
        .readTimeout(0, TimeUnit.SECONDS)
        .pingInterval(20, TimeUnit.SECONDS)
        .build()
    private val JSON = "application/json".toMediaType()

    /** Pairs this phone using the 6-digit code from the dashboard. */
    fun pair(server: String, code: String, name: String, identity: Identity, platform: String): Pairing {
        val base = server.trimEnd('/')
        val body = buildJsonObject {
            put("code", code.trim()); put("name", name); put("pubkey", identity.publicKeyB64); put("platform", platform)
        }.toString().toRequestBody(JSON)
        http.newCall(Request.Builder().url("$base/api/devices/pair").post(body).build()).execute().use { r ->
            val v = Json.parseToJsonElement(r.body?.string() ?: "{}").jsonObject
            if (!r.isSuccessful) {
                val msg = (v["error"] as? JsonObject)?.get("message")?.jsonPrimitive?.content ?: "pairing failed (${r.code})"
                throw IllegalStateException(msg)
            }
            return Pairing(base, v["device_id"]!!.jsonPrimitive.content, v["token"]!!.jsonPrimitive.content)
        }
    }

    /** Earnings, dependability and class for the status screen. */
    fun deviceMe(p: Pairing): JsonObject? = runCatching {
        val req = Request.Builder().url("${p.server}/api/device/me?device_id=${p.deviceId}")
            .header("Authorization", "Bearer ${p.token}").build()
        http.newCall(req).execute().use { Json.parseToJsonElement(it.body!!.string()).jsonObject }
    }.getOrNull()
}
