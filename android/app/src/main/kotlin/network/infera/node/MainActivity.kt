package network.infera.node

import android.Manifest
import android.app.Activity
import android.graphics.Color
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.text.InputType
import android.view.Gravity
import android.view.View
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.Switch
import android.widget.TextView
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.longOrNull
import kotlinx.serialization.json.doubleOrNull
import okhttp3.Request
import java.io.File

/** One screen: pair → get a model → benchmark → run, with the Uber-style offer card. */
class MainActivity : Activity() {
    private lateinit var prefs: Prefs
    private lateinit var root: LinearLayout
    private val ui = Handler(Looper.getMainLooper())
    private val green = Color.rgb(0x17, 0x79, 0x5a)
    private val amber = Color.rgb(0xa8, 0x6a, 0x00)
    private var remote = ""
    private var stopArmed = false
    private val listener: (NodeStatus) -> Unit = { ui.post { if (prefs.ready) render() } }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        prefs = Prefs(this)
        if (Build.VERSION.SDK_INT >= 33) requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        root = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(dp(18), dp(24), dp(18), dp(24)) }
        setContentView(ScrollView(this).apply { addView(root) })
        NodeService.listeners.add(listener)
        render()
        ui.post(object : Runnable {
            override fun run() { if (prefs.ready) { refreshRemote(); render() }; ui.postDelayed(this, 1000) }
        })
    }

    override fun onDestroy() { NodeService.listeners.remove(listener); super.onDestroy() }

    private fun dp(v: Int) = (v * resources.displayMetrics.density).toInt()

    private fun text(s: String, size: Float = 15f, bold: Boolean = false, color: Int? = null) = TextView(this).apply {
        text = s; textSize = size; if (bold) setTypeface(typeface, Typeface.BOLD); color?.let { setTextColor(it) }
        setPadding(0, dp(4), 0, dp(4))
    }
    private fun field(hint: String, value: String = "", number: Boolean = false) = EditText(this).apply {
        this.hint = hint; setText(value); if (number) inputType = InputType.TYPE_CLASS_NUMBER or InputType.TYPE_NUMBER_FLAG_DECIMAL
    }
    private fun button(label: String, primary: Boolean = true, onClick: () -> Unit) = Button(this).apply {
        text = label; isAllCaps = false; setOnClickListener { onClick() }
        if (primary) { setBackgroundColor(green); setTextColor(Color.WHITE) }
    }
    private fun card(vararg views: View) = LinearLayout(this).apply {
        orientation = LinearLayout.VERTICAL; setPadding(dp(16), dp(14), dp(16), dp(14))
        background = GradientDrawable().apply { cornerRadius = dp(14).toFloat(); setStroke(dp(1), Color.rgb(0xc9, 0xd3, 0xce)) }
        layoutParams = LinearLayout.LayoutParams(-1, -2).apply { topMargin = dp(12) }
        views.forEach { addView(it) }
    }

    private fun render() {
        root.removeAllViews()
        root.addView(text("Infera", 26f, true))
        when {
            !prefs.paired -> renderPair()
            !prefs.ready -> renderModel()
            else -> renderRunning()
        }
    }

    private fun renderPair() {
        val server = field("Server, e.g. https://beta.example.com", prefs.server)
        val code = field("6-digit code from the dashboard", number = true)
        val status = text("")
        root.addView(text("Earn by letting your phone serve AI jobs while it charges."))
        root.addView(card(text("Pair this phone", 17f, true), server, code, status, button("Pair") {
            status.text = "Pairing…"
            Thread {
                try {
                    val p = Api.pair(server.text.toString(), code.text.toString(), Build.MODEL, prefs.identity, "android ${Build.VERSION.RELEASE}")
                    prefs.server = p.server; prefs.deviceId = p.deviceId; prefs.token = p.token
                    ui.post { render() }
                } catch (e: Exception) { ui.post { status.text = e.message } }
            }.start()
        }))
    }

    private fun renderModel() {
        val url = field("Model file URL (.gguf)", "")
        val id = field("Model name, e.g. qwen3-1.7b", prefs.modelId)
        val params = field("Size in billions, e.g. 1.7", if (prefs.paramsB > 0) prefs.paramsB.toString() else "", number = true)
        val status = text(if (prefs.klass == "unrated") "Too slow with that model (below 3 tokens/s). Try a smaller one." else "")
        root.addView(text("Use the same model name as other testers so jobs can reach your phone."))
        root.addView(card(text("Choose a model", 17f, true), url, id, params, status, button("Download and benchmark") {
            val u = url.text.toString().trim(); val mid = id.text.toString().trim(); val pb = params.text.toString().toFloatOrNull() ?: 0f
            if (mid.isEmpty() || pb <= 0f) { status.text = "Enter the model name and size."; return@button }
            Thread { setupModel(u, mid, pb) { s -> ui.post { status.text = s } } }.start()
        }))
    }

    private fun setupModel(url: String, modelId: String, paramsB: Float, say: (String) -> Unit) {
        try {
            val dir = File(filesDir, "models").apply { mkdirs() }
            val file = File(dir, "$modelId.gguf")
            if (url.isNotEmpty()) {
                Api.http.newCall(Request.Builder().url(url).build()).execute().use { r ->
                    if (!r.isSuccessful) throw IllegalStateException("download failed (${r.code})")
                    val total = r.body!!.contentLength()
                    val tmp = File(dir, "$modelId.part")
                    r.body!!.byteStream().use { input -> tmp.outputStream().use { out ->
                        val buf = ByteArray(1 shl 16); var done = 0L; var last = 0L
                        while (true) {
                            val n = input.read(buf); if (n < 0) break
                            out.write(buf, 0, n); done += n
                            if (done - last > 20_000_000) { last = done; say("Downloading… ${done / 1_000_000} MB" + if (total > 0) " of ${total / 1_000_000} MB" else "") }
                        }
                    } }
                    tmp.renameTo(file)
                }
            }
            if (!file.exists()) throw IllegalStateException("enter a model URL to download")
            say("Loading the model…")
            NodeService.engine?.close()
            val eng = LlamaEngine(file.path, modelId)
            NodeService.engine = eng
            say("Benchmarking (about a minute)…")
            var first = 0L; var n = 0
            eng.generate(listOf("user" to "Write a short paragraph about how rivers shape valleys."), 64, 0f) {
                if (first == 0L) first = System.nanoTime(); n++; true
            }
            val secs = ((System.nanoTime() - first) / 1e9).coerceAtLeast(0.001)
            val tps = (n - 1).coerceAtLeast(0) / secs
            val ram = (getSystemService(android.app.ActivityManager::class.java)).let { val mi = android.app.ActivityManager.MemoryInfo(); it.getMemoryInfo(mi); mi.totalMem / 1e9 }
            prefs.modelPath = file.path; prefs.modelId = modelId; prefs.paramsB = paramsB; prefs.tps = tps.toFloat()
            prefs.klass = phoneClass(tps, ram)
            say("%.1f tokens/s · class %s".format(tps, prefs.klass))
            ui.post { render() }
        } catch (e: Exception) { say("Problem: ${e.message}") }
    }

    private fun refreshRemote() {
        if (System.currentTimeMillis() / 1000 % 15 != 0L) return
        Thread {
            val v = Api.deviceMe(prefs.pairing) ?: return@Thread
            val today = v["today"]?.jsonObject
            val bal = v["balance"]?.jsonObject
            val dev = v["device"]?.jsonObject
            fun usd(m: Long?) = if (m == null) "–" else "$" + "%.${if (m != 0L && kotlin.math.abs(m) < 10_000) 6 else 2}f".format(m / 1e6)
            remote = "Today ${usd(today?.get("earned_today")?.jsonPrimitive?.longOrNull)} · ${today?.get("jobs_today")?.jsonPrimitive?.content ?: "0"} jobs\n" +
                "Available ${usd(bal?.get("available")?.jsonPrimitive?.longOrNull)} · held ${usd(bal?.get("held")?.jsonPrimitive?.longOrNull)}\n" +
                "Class ${dev?.get("class")?.jsonPrimitive?.content ?: prefs.klass} · dependability ${"%.1f".format(dev?.get("dependability")?.jsonPrimitive?.doubleOrNull ?: 0.0)}"
        }.start()
    }

    private fun renderRunning() {
        val n = NodeService.node
        val s = n?.status
        val running = prefs.enabled && n != null
        val headline = when {
            !running -> "Not running"
            s?.runningJob != null -> "Working on a job"
            s?.connected != true -> "Connecting…"
            s.status == "idle" -> "Online · waiting for jobs"
            else -> "Paused · ${s.pauseReason}"
        }
        root.addView(card(
            text(headline, 18f, true, if (s?.runningJob != null) amber else green),
            text(remote.ifEmpty { "Model ${prefs.modelId} · %.1f tok/s · class ${prefs.klass}".format(prefs.tps) }),
            if (!running) button("Start earning") { prefs.enabled = true; NodeService.start(this); ui.postDelayed({ render() }, 800) }
            else button(if (s?.online == false) "Go online" else "Go offline", primary = false) { n.setOnline(s?.online == false) },
        ))

        s?.offer?.let { o ->
            root.addView(card(
                text("NEW JOB OFFER · ${o.secondsLeft()}s", 12f, color = Color.GRAY),
                text("$%.4f".format(o.estPayUsd), 34f, true, amber),
                text("≈ $%.2f per hour".format(o.perHourUsd)),
                text("${o.jobType.replace('_', ' ')} · ${o.model} · about ${o.estSeconds.toInt().coerceAtLeast(1)} s"),
                button("Accept") { n?.decide(true) },
                button("Decline", primary = false) { n?.decide(false) },
                text("Declining is free. Once accepted, the job must finish.", 12f, color = Color.GRAY),
            ).apply { background = GradientDrawable().apply { cornerRadius = dp(14).toFloat(); setStroke(dp(2), green) } })
        }

        if (s?.runningJob != null) {
            root.addView(card(
                text("Running job · sealed · earns ~$%.4f".format(s.runningPayUsd), 15f, true),
                button("Finish this job, then go offline", primary = false) { n?.finishThenOffline() },
                button(if (stopArmed) "Tap again: stop now and take the penalty" else "Stop now", primary = false) {
                    if (stopArmed) { n?.stopNow(); stopArmed = false } else stopArmed = true
                },
                text("Stopping now loses this job's pay, debits 2× its value and costs 10 dependability points.", 12f, color = Color.GRAY),
            ))
        }

        val ask = Switch(this).apply { text = "Ask before every job"; isChecked = prefs.mode == "ask"
            setOnCheckedChangeListener { _, on -> prefs.mode = if (on) "ask" else "auto"; n?.setMode(prefs.mode) } }
        val screen = Switch(this).apply { text = "Also work while the screen is on (Charge Mode)"; isChecked = prefs.workWithScreenOn
            setOnCheckedChangeListener { _, on -> prefs.workWithScreenOn = on } }
        root.addView(card(ask, screen, text("Jobs run only while charging, on Wi-Fi, above 50% battery and not warm.", 12f, color = Color.GRAY)))
        if (running) root.addView(button("Stop the Infera service", primary = false) {
            startService(android.content.Intent(this, NodeService::class.java).setAction(NodeService.ACTION_STOP))
            ui.postDelayed({ render() }, 800)
        })
        root.addView(card(text("Activity", 13f, true), text((s?.events ?: emptyList()).take(12).joinToString("\n"), 12f).apply { gravity = Gravity.START }))
    }
}
