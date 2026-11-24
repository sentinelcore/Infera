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

/** One entry of the server's model catalog (GET /api/catalog). */
data class CatalogModel(
    val id: String, val label: String, val kind: String, val paramsB: Double,
    val ggufUrl: String, val ggufMb: Int, val phoneMinRamGb: Double,
)

const val DEFAULT_SERVER = "https://control-plane-production-11856175.up.railway.app"

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

    /** Models phones can download (chat models with a GGUF file), from the server's catalog. */
    fun catalog(server: String): List<CatalogModel> {
        val req = Request.Builder().url("${server.trimEnd('/')}/api/catalog").build()
        http.newCall(req).execute().use { r ->
            val v = Json.parseToJsonElement(r.body?.string() ?: "{}").jsonObject
            val arr = v["models"] as? kotlinx.serialization.json.JsonArray ?: return emptyList()
            return arr.mapNotNull { e ->
                val o = e.jsonObject
                fun s(k: String) = o[k]?.jsonPrimitive?.content ?: ""
                fun d(k: String) = s(k).toDoubleOrNull() ?: 0.0
                val url = s("gguf_url")
                if (url.isEmpty() || s("kind") != "chat") null
                else CatalogModel(s("id"), s("label"), s("kind"), d("params_b"), url, d("gguf_mb").toInt(), d("phone_min_ram_gb"))
            }
        }
    }

    /** Best model for a phone with this much RAM: the largest one it meets the minimum for. */
    fun pickForPhone(models: List<CatalogModel>, ramGb: Double): CatalogModel? =
        models.filter { it.phoneMinRamGb <= ramGb }.maxByOrNull { it.paramsB } ?: models.minByOrNull { it.paramsB }

    /** Earnings, dependability and class for the status screen. */
    fun deviceMe(p: Pairing): JsonObject? = runCatching {
        val req = Request.Builder().url("${p.server}/api/device/me?device_id=${p.deviceId}")
            .header("Authorization", "Bearer ${p.token}").build()
        http.newCall(req).execute().use { Json.parseToJsonElement(it.body!!.string()).jsonObject }
    }.getOrNull()
}
