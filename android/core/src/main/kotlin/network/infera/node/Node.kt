package network.infera.node

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.doubleOrNull
import kotlinx.serialization.json.floatOrNull
import kotlinx.serialization.json.intOrNull
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.longOrNull
import kotlinx.serialization.json.put
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean

/** Inference engine on the device (llama.cpp on phones). */
interface Engine {
    val modelId: String
    /** Streams text; return false from onText to stop. Returns (prompt tokens, completion tokens). */
    fun generate(messages: List<Pair<String, String>>, maxTokens: Int, temperature: Float, onText: (String) -> Boolean): Pair<Int, Int>
}

/** Speech to text (Whisper). Returns (result JSON {"text","duration",...}, audio seconds). */
interface SpeechEngine {
    val modelId: String
    fun transcribe(audio: ByteArray, filename: String, language: String, prompt: String): Pair<String, Int>
}

data class Offer(
    val jobId: String, val jobType: String, val model: String, val estPayUsd: Double,
    val estSeconds: Double, val ttlSeconds: Int, val receivedAt: Long = System.currentTimeMillis(),
) {
    val perHourUsd: Double get() = if (estSeconds > 0) estPayUsd / estSeconds * 3600 else 0.0
    fun secondsLeft(): Int = (ttlSeconds - (System.currentTimeMillis() - receivedAt) / 1000).toInt().coerceAtLeast(0)
}

/** Auto-approve limits: an offer outside them becomes an offer card even in auto mode. 0 = no limit. */
data class AutoLimits(val minPayUsd: Double = 0.0, val minPerHourUsd: Double = 0.0, val maxMinutes: Double = 0.0) {
    fun outside(o: Offer): String? = when {
        minPayUsd > 0 && o.estPayUsd < minPayUsd -> "pays $%.4f, below your $%.4f minimum".format(o.estPayUsd, minPayUsd)
        minPerHourUsd > 0 && o.estSeconds > 0 && o.perHourUsd < minPerHourUsd -> "pays $%.2f/hour, below your $%.2f/hour minimum".format(o.perHourUsd, minPerHourUsd)
        maxMinutes > 0 && o.estSeconds / 60 > maxMinutes -> "takes about %.1f min, longer than your %.0f min limit".format(o.estSeconds / 60, maxMinutes)
        else -> null
    }
}

data class Bench(val tokensPerSecond: Double, val paramsB: Double, val ramGb: Double, val klass: String)

/** What the UI shows; updated on every change. */
data class NodeStatus(
    val connected: Boolean = false,
    val online: Boolean = true,
    val mode: String = "auto",
    val status: String = "paused",
    val pauseReason: String = "",
    val runningJob: String? = null,
    val runningPayUsd: Double = 0.0,
    val offer: Offer? = null,
    val hub: JsonObject? = null,
    val events: List<String> = emptyList(),
)

/**
 * The phone node: WebSocket link to the hub, offers, sealed jobs and signed receipts.
 * `readiness` returns null when the phone may take new jobs, otherwise the reason it can't.
 */
class Node(
    private val pairing: Pairing,
    private val identity: Identity,
    private val engine: Engine,
    private val bench: Bench,
    private val platform: String,
    private val readiness: () -> String?,
    private val onStatus: (NodeStatus) -> Unit,
) {
    @Volatile var status = NodeStatus()
        private set
    private var ws: WebSocket? = null
    private val running = AtomicBoolean(false)
    private val jobs = Executors.newSingleThreadExecutor()
    private val ticker = Executors.newSingleThreadScheduledExecutor()
    private val payloads = ConcurrentHashMap<String, (Triple<String, String, String>) -> Unit>()
    private val cancels = ConcurrentHashMap<String, AtomicBoolean>()
    @Volatile var limits = AutoLimits()
    /** Optional speech engine: when set, this phone also takes transcription jobs. */
    @Volatile var speech: SpeechEngine? = null
    @Volatile private var decision: ((Boolean) -> Unit)? = null
    @Volatile private var drainThenOffline = false
    @Volatile private var sentState = ""

    fun start() {
        if (!running.compareAndSet(false, true)) return
        connect()
        ticker.scheduleWithFixedDelay({ tick() }, 1, 2, TimeUnit.SECONDS)
    }

    fun stop() {
        running.set(false)
        send(buildJsonObject { put("t", "bye"); put("reason", "drain_complete") })
        ws?.close(1000, "bye")
        ticker.shutdownNow()
        jobs.shutdownNow()
    }

    fun setOnline(on: Boolean) = update { it.copy(online = on) }.also { pushState(force = true) }
    fun setMode(mode: String) = update { it.copy(mode = mode) }.also { pushState(force = true) }
    fun decide(accept: Boolean) { decision?.invoke(accept); decision = null }
    fun finishThenOffline() { if (status.runningJob == null) setOnline(false) else drainThenOffline = true; log("Finishing the current job, then going offline") }

    /** Stop the running job now: counts as an owner abort (pay lost, debit, −10 dependability). */
    fun stopNow() {
        val job = status.runningJob ?: return
        send(buildJsonObject { put("t", "abort"); put("job_id", job); put("reason", "owner_stop") })
        cancels[job]?.set(true)
        update { it.copy(online = false) }
        log("Stopped job $job (owner abort)")
    }

    private fun connect() {
        val url = pairing.server.replaceFirst("https://", "wss://").replaceFirst("http://", "ws://") +
            "/ws/node?device_id=${pairing.deviceId}&token=${pairing.token}"
        ws = Api.http.newWebSocket(Request.Builder().url(url).build(), object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) {
                update { it.copy(connected = true) }
                log("Connected to ${pairing.server}")
                send(buildJsonObject {
                    put("t", "hello"); put("agent_version", "android-0.1.0"); put("platform", platform)
                    put("class", bench.klass); put("privacy", "sandboxed"); put("mode", status.mode); put("tps", bench.tokensPerSecond)
                    put("models", JsonArray(listOfNotNull(engine.modelId, speech?.modelId).map { JsonPrimitive(it) }))
                    put("bench", buildJsonObject {
                        put("tokens_per_s", bench.tokensPerSecond); put("params_b", bench.paramsB); put("ram_gb", bench.ramGb)
                        put("class", bench.klass); put("model_id", engine.modelId)
                    })
                })
                pushState(force = true)
            }
            override fun onMessage(webSocket: WebSocket, text: String) = handle(Json.parseToJsonElement(text).jsonObject)
            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) = reconnect("Connection problem: ${t.message}")
            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = reconnect("Disconnected")
        })
    }

    private fun reconnect(why: String) {
        update { it.copy(connected = false) }
        if (!running.get()) return
        log("$why; retrying in 5 s")
        ticker.schedule({ if (running.get()) connect() }, 5, TimeUnit.SECONDS)
    }

    private fun handle(m: JsonObject) {
        val t = m["t"]?.jsonPrimitive?.contentOrNull ?: return
        val jobId = m["job_id"]?.jsonPrimitive?.contentOrNull ?: ""
        when (t) {
            "config" -> update { it.copy(hub = m) }
            "offer" -> jobs.execute { onOffer(m) }
            "payload" -> payloads.remove(jobId)?.invoke(Triple(m.s("epk"), m.s("n"), m.s("c")))
            "cancel" -> { cancels[jobId]?.set(true); payloads.remove(jobId) }
            "offer_expired" -> if (status.offer?.jobId == jobId) { update { it.copy(offer = null) }; decision = null }
        }
    }

    private fun onOffer(m: JsonObject) {
        val jobId = m.s("job_id")
        val model = m.s("model")
        if (status.status != "idle") { decline(jobId, "governor"); return }
        val estTokens = m["est_tokens"]?.jsonPrimitive?.doubleOrNull ?: 0.0
        val offer = Offer(jobId, m.s("job_type").ifEmpty { "chat" }, model, m["est_pay_usd"]?.jsonPrimitive?.doubleOrNull ?: 0.0,
            estTokens / bench.tokensPerSecond.coerceAtLeast(1.0), m["ttl_s"]?.jsonPrimitive?.intOrNull ?: 30)
        val why = limits.outside(offer)
        if (status.mode == "auto" && why != null) log("Offer needs your answer: it $why")
        val accepted = if (status.mode == "auto" && why == null) true else {
            val latch = CountDownLatch(1)
            var answer = false
            decision = { a -> answer = a; latch.countDown() }
            update { it.copy(offer = offer) }
            log("New job offer: $%.4f".format(offer.estPayUsd))
            latch.await(offer.ttlSeconds.toLong(), TimeUnit.SECONDS)
            decision = null
            update { it.copy(offer = null) }
            answer
        }
        if (!accepted || readiness() != null) { decline(jobId, if (accepted) "governor" else "user_declined"); return }

        val key = SandboxKey()
        val payloadLatch = CountDownLatch(1)
        var payload: Triple<String, String, String>? = null
        payloads[jobId] = { p -> payload = p; payloadLatch.countDown() }
        val cancel = AtomicBoolean(false)
        cancels[jobId] = cancel
        update { it.copy(status = "busy", runningJob = jobId, runningPayUsd = offer.estPayUsd) }
        pushState(force = true)
        send(buildJsonObject { put("t", "accept"); put("job_id", jobId); put("sandbox_pub", key.publicB64) })
        try {
            if (!payloadLatch.await(20, TimeUnit.SECONDS)) throw IllegalStateException("job payload never arrived")
            val (epk, n, c) = payload!!
            if (offer.jobType == "transcription") runSpeech(jobId, model, key.open(jobId, epk), n, c)
            else runJob(jobId, model, key.open(jobId, epk), n, c, cancel)
        } catch (e: Exception) {
            log("Job $jobId failed: ${e.message}")
        } finally {
            cancels.remove(jobId)
            payloads.remove(jobId)
            val off = drainThenOffline
            drainThenOffline = false
            update { it.copy(runningJob = null, status = "paused", online = if (off) false else it.online) }
            if (off) log("Job finished; now offline")
            tick()
        }
    }

    /** Transcription job: decode the sealed audio, run Whisper, return one sealed result. */
    private fun runSpeech(jobId: String, model: String, chan: SealedChannel, n: String, c: String) {
        val sp = speech ?: run {
            send(buildJsonObject { put("t", "abort"); put("job_id", jobId); put("reason", "engine_error") })
            throw IllegalStateException("no speech model on this phone")
        }
        val req = Json.parseToJsonElement(String(chan.openDown(n, c))).jsonObject
        fun s(k: String) = req[k]?.jsonPrimitive?.contentOrNull ?: ""
        val audio = java.util.Base64.getDecoder().decode(s("audio_b64"))
        log("Running transcription job $jobId ($model) in the sealed sandbox")
        val started = System.currentTimeMillis()
        val (out, secs) = try {
            sp.transcribe(audio, s("filename"), s("language"), s("prompt"))
        } catch (e: Exception) {
            send(buildJsonObject { put("t", "abort"); put("job_id", jobId); put("reason", "engine_error") })
            throw e
        }
        val (nonce, ct) = chan.sealUp(out.toByteArray())
        send(buildJsonObject { put("t", "tok"); put("job_id", jobId); put("seq", 1); put("n", nonce); put("c", ct) })
        val receipt = "$jobId|$model|${sha256Hex(out.toByteArray())}|$secs|0|${System.currentTimeMillis() / 1000}"
        send(buildJsonObject {
            put("t", "done"); put("job_id", jobId); put("tokens_in", secs); put("tokens_out", 0)
            put("receipt", receipt); put("sig", identity.sign(receipt))
        })
        log("Transcription job $jobId done: $secs s of audio in %.1f s".format((System.currentTimeMillis() - started) / 1000.0))
    }

    private fun runJob(jobId: String, model: String, chan: SealedChannel, n: String, c: String, cancel: AtomicBoolean) {
        val req = Json.parseToJsonElement(String(chan.openDown(n, c))).jsonObject
        val msgs = (req["messages"] as? JsonArray ?: JsonArray(emptyList())).map {
            val o = it.jsonObject
            (o["role"]?.jsonPrimitive?.contentOrNull ?: "user") to (o["content"]?.jsonPrimitive?.contentOrNull ?: "")
        }
        val maxTokens = req["max_tokens"]?.jsonPrimitive?.intOrNull ?: 512
        val temp = req["temperature"]?.jsonPrimitive?.floatOrNull ?: 0.7f
        log("Running job $jobId ($model) in the sealed sandbox")
        val out = StringBuilder()
        var seq = 0
        val started = System.currentTimeMillis()
        val (tin, tout) = try {
            engine.generate(msgs, maxTokens, temp) { piece ->
                if (cancel.get()) return@generate false
                out.append(piece)
                seq++
                val (nonce, ct) = chan.sealUp(piece.toByteArray())
                send(buildJsonObject { put("t", "tok"); put("job_id", jobId); put("seq", seq); put("n", nonce); put("c", ct) })
                true
            }
        } catch (e: Exception) {
            send(buildJsonObject { put("t", "abort"); put("job_id", jobId); put("reason", "engine_error") })
            throw e
        }
        if (cancel.get()) { log("Job $jobId stopped"); return }
        if (tin < 0) {
            send(buildJsonObject { put("t", "abort"); put("job_id", jobId); put("reason", "engine_error") })
            throw IllegalStateException("engine error")
        }
        val receipt = "$jobId|$model|${sha256Hex(out.toString().toByteArray())}|$tin|$tout|${System.currentTimeMillis() / 1000}"
        send(buildJsonObject {
            put("t", "done"); put("job_id", jobId); put("tokens_in", tin); put("tokens_out", tout)
            put("receipt", receipt); put("sig", identity.sign(receipt))
        })
        log("Job $jobId done: $tout tokens in %.1f s".format((System.currentTimeMillis() - started) / 1000.0))
    }

    private fun decline(jobId: String, reason: String) =
        send(buildJsonObject { put("t", "decline"); put("job_id", jobId); put("reason", reason) })

    /** Governor tick: recompute idle/paused and tell the hub when it changes. */
    private fun tick() {
        val reason = when {
            !status.online -> "you are offline"
            suspended() -> "suspended by Infera (see dashboard)"
            else -> readiness()
        }
        if (status.runningJob == null) update { it.copy(status = if (reason == null) "idle" else "paused", pauseReason = reason ?: "") }
        pushState()
    }

    private fun suspended(): Boolean =
        (status.hub?.get("suspended_until")?.jsonPrimitive?.longOrNull ?: 0L) > System.currentTimeMillis() / 1000

    private fun pushState(force: Boolean = false) {
        val s = status
        val key = "${s.status}|${s.online}|${s.mode}"
        if (!force && key == sentState) return
        sentState = key
        send(buildJsonObject { put("t", "state"); put("status", s.status); put("online", s.online); put("mode", s.mode) })
    }

    private fun send(o: JsonObject) { ws?.send(o.toString()) }

    private fun log(line: String) {
        val t = java.text.SimpleDateFormat("HH:mm:ss", java.util.Locale.US).format(java.util.Date())
        update { it.copy(events = (listOf("$t $line") + it.events).take(30)) }
    }

    @Synchronized private fun update(f: (NodeStatus) -> NodeStatus) {
        status = f(status)
        onStatus(status)
    }

    private fun JsonObject.s(k: String) = this[k]?.jsonPrimitive?.contentOrNull ?: ""
}

/** Phone classes (ARCHITECTURE.md §3.1). */
fun phoneClass(tokensPerSecond: Double, ramGb: Double): String = when {
    tokensPerSecond >= 8 && ramGb >= 7.5 -> "M1"
    tokensPerSecond >= 3 -> "M2"
    else -> "unrated"
}
