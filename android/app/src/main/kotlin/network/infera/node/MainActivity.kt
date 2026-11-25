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
    companion object {
        /** Model download/benchmark state, shared across activity instances (rotation, reopening). */
        @Volatile var setupBusy = false
        @Volatile var setupStatus = ""
        @Volatile var setupPct = -1
        @Volatile var current: MainActivity? = null
        @Volatile var pricing = Pricing()
        /** HTTP/1.1 only: long HTTP/2 downloads from the model CDN get "stream was reset: PROTOCOL_ERROR". */
        val downloadHttp: okhttp3.OkHttpClient by lazy {
            Api.http.newBuilder().protocols(listOf(okhttp3.Protocol.HTTP_1_1))
                .readTimeout(60, java.util.concurrent.TimeUnit.SECONDS).build()
        }
    }
    private var statusView: TextView? = null
    private lateinit var prefs: Prefs
    private lateinit var root: LinearLayout
    private val ui = Handler(Looper.getMainLooper())
    private val green = Color.rgb(0x17, 0x79, 0x5a)
    private val amber = Color.rgb(0xa8, 0x6a, 0x00)
    private var tab = "home"            // home | device | settings
    private var benchJustDone = false
    private var rToday = ""; private var rJobs = ""; private var rAvail = ""; private var rHeld = ""; private var rDep = ""
    private val dark get() = (resources.configuration.uiMode and android.content.res.Configuration.UI_MODE_NIGHT_MASK) == android.content.res.Configuration.UI_MODE_NIGHT_YES
    private val bg get() = if (dark) Color.rgb(0x0e, 0x14, 0x12) else Color.rgb(0xf4, 0xf6, 0xf4)
    private val surface get() = if (dark) Color.rgb(0x16, 0x1f, 0x1b) else Color.WHITE
    private val fg get() = if (dark) Color.rgb(0xe3, 0xeb, 0xe7) else Color.rgb(0x14, 0x20, 0x1b)
    private val muted get() = if (dark) Color.rgb(0x9a, 0xab, 0xa3) else Color.rgb(0x56, 0x65, 0x5e)
    private val line get() = if (dark) Color.rgb(0x2f, 0x3e, 0x37) else Color.rgb(0xc9, 0xd3, 0xce)
    private val red = Color.rgb(0xb3, 0x41, 0x2e)
    private var stopArmed = false
    private val listener: (NodeStatus) -> Unit = { ui.post { if (prefs.ready) render() } }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        prefs = Prefs(this)
        current = this
        if (Build.VERSION.SDK_INT >= 33) requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        root = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(dp(18), dp(24), dp(18), dp(24)) }
        actionBar?.hide() // the app draws its own "Infera" header
        val scroll = ScrollView(this).apply { setBackgroundColor(bg); addView(root) }
        // Android 15 draws apps edge to edge: keep content clear of the status bar and gesture bar.
        scroll.setOnApplyWindowInsetsListener { _, insets ->
            val (top, bottom) = if (Build.VERSION.SDK_INT >= 30) {
                val b = insets.getInsets(android.view.WindowInsets.Type.systemBars() or android.view.WindowInsets.Type.displayCutout())
                b.top to b.bottom
            } else {
                @Suppress("DEPRECATION") (insets.systemWindowInsetTop to insets.systemWindowInsetBottom)
            }
            root.setPadding(dp(18), top + dp(12), dp(18), bottom + dp(24))
            insets
        }
        window.statusBarColor = bg
        window.navigationBarColor = bg
        if (!dark && Build.VERSION.SDK_INT >= 30) window.insetsController?.setSystemBarsAppearance(
            android.view.WindowInsetsController.APPEARANCE_LIGHT_STATUS_BARS or android.view.WindowInsetsController.APPEARANCE_LIGHT_NAVIGATION_BARS,
            android.view.WindowInsetsController.APPEARANCE_LIGHT_STATUS_BARS or android.view.WindowInsetsController.APPEARANCE_LIGHT_NAVIGATION_BARS)
        setContentView(scroll)
        NodeService.listeners.add(listener)
        render()
        handlePairLink(intent)
        ui.post(object : Runnable {
            override fun run() { if (prefs.ready) { refreshRemote(); render() }; ui.postDelayed(this, 1000) }
        })
    }

    override fun onNewIntent(intent: android.content.Intent) {
        super.onNewIntent(intent)
        handlePairLink(intent)
    }

    /** infera://pair?server=…&code=… — pairs with no typing (dashboard "Pair this phone"). */
    private fun handlePairLink(i: android.content.Intent?) {
        val data = i?.data ?: return
        if (data.scheme != "infera" || data.host != "pair") return
        val server = data.getQueryParameter("server") ?: return
        val code = data.getQueryParameter("code") ?: return
        i.data = null
        pairWith(server, code) { msg -> ui.post { android.widget.Toast.makeText(this, msg, android.widget.Toast.LENGTH_LONG).show() } }
    }

    private fun pairWith(server: String, code: String, say: (String) -> Unit) {
        say("Pairing…")
        Thread {
            try {
                if (prefs.paired) { // pairing again: stop the old link first
                    startService(android.content.Intent(this, NodeService::class.java).setAction(NodeService.ACTION_STOP))
                    prefs.enabled = false
                }
                val p = Api.pair(server, code, Build.MODEL, prefs.identity, "android ${Build.VERSION.RELEASE}", hardwareId())
                prefs.server = p.server; prefs.deviceId = p.deviceId; prefs.token = p.token
                say("Paired")
                ui.post { render() }
            } catch (e: Exception) { say(e.message ?: "Pairing failed") }
        }.start()
    }

    override fun onDestroy() { NodeService.listeners.remove(listener); if (current === this) current = null; super.onDestroy() }

    private fun dp(v: Int) = (v * resources.displayMetrics.density).toInt()

    private fun text(s: String, size: Float = 15f, bold: Boolean = false, color: Int? = null) = TextView(this).apply {
        text = s; textSize = size; if (bold) setTypeface(typeface, Typeface.BOLD); setTextColor(color ?: fg)
        setPadding(0, dp(4), 0, dp(4))
    }
    /** Label on the left, value on the right. */
    private fun row(label: String, value: String, valueColor: Int? = null) = LinearLayout(this).apply {
        orientation = LinearLayout.HORIZONTAL; setPadding(0, dp(3), 0, dp(3))
        addView(text(label, 14f, color = muted), LinearLayout.LayoutParams(0, -2, 1f))
        addView(text(value, 14f, true, valueColor).apply { gravity = Gravity.END })
    }
    /** Checklist line: ✓ / ✗, label, and a short explanation. */
    private fun checkRow(ok: Boolean, label: String, detail: String) = LinearLayout(this).apply {
        orientation = LinearLayout.HORIZONTAL; setPadding(0, dp(5), 0, dp(5)); gravity = Gravity.CENTER_VERTICAL
        addView(text(if (ok) "✓" else "✗", 18f, true, if (ok) green else red).apply { minWidth = dp(28) })
        addView(LinearLayout(this@MainActivity).apply {
            orientation = LinearLayout.VERTICAL
            addView(text(label, 15f, true).apply { setPadding(0, 0, 0, 0) })
            addView(text(detail, 12f, color = muted).apply { setPadding(0, 0, 0, 0) })
        })
    }
    private fun pill(s: String, color: Int) = TextView(this).apply {
        text = s; textSize = 12f; setTypeface(typeface, Typeface.BOLD); setTextColor(color)
        setPadding(dp(10), dp(3), dp(10), dp(3))
        background = GradientDrawable().apply { cornerRadius = dp(99).toFloat(); setStroke(dp(1), color) }
        layoutParams = LinearLayout.LayoutParams(-2, -2)
    }
    private fun heading(s: String) = text(s, 13f, true, muted).apply { setPadding(0, dp(18), 0, 0); setAllCaps(true); letterSpacing = 0.06f }
    private fun field(hint: String, value: String = "", number: Boolean = false) = EditText(this).apply {
        this.hint = hint; setText(value); if (number) inputType = InputType.TYPE_CLASS_NUMBER or InputType.TYPE_NUMBER_FLAG_DECIMAL
    }
    private fun button(label: String, primary: Boolean = true, onClick: () -> Unit) = Button(this).apply {
        text = label; isAllCaps = false; setOnClickListener { onClick() }
        if (primary) { setBackgroundColor(green); setTextColor(Color.WHITE) }
    }
    private fun card(vararg views: View) = LinearLayout(this).apply {
        orientation = LinearLayout.VERTICAL; setPadding(dp(16), dp(14), dp(16), dp(14))
        background = GradientDrawable().apply { cornerRadius = dp(14).toFloat(); setColor(surface); setStroke(dp(1), line) }
        layoutParams = LinearLayout.LayoutParams(-1, -2).apply { topMargin = dp(12) }
        views.forEach { addView(it) }
    }

    private fun render() {
        root.removeAllViews()
        root.addView(LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL; gravity = Gravity.CENTER_VERTICAL
            addView(text("Infera", 26f, true), LinearLayout.LayoutParams(0, -2, 1f))
            if (prefs.ready) addView(pill("Class ${prefs.klass}", green))
        })
        when {
            !prefs.paired -> renderPair()
            !prefs.ready -> renderModel()
            else -> {
                root.addView(tabs())
                when (tab) {
                    "device" -> renderDevice()
                    "settings" -> renderSettings()
                    else -> renderHome()
                }
            }
        }
        root.addView(text("Version ${BuildConfig.VERSION_NAME} · built ${BuildConfig.BUILD_TIME}", 11f, color = muted).apply { setPadding(0, dp(20), 0, 0); gravity = Gravity.CENTER })
    }

    private fun tabs() = LinearLayout(this).apply {
        orientation = LinearLayout.HORIZONTAL
        layoutParams = LinearLayout.LayoutParams(-1, -2).apply { topMargin = dp(8) }
        background = GradientDrawable().apply { cornerRadius = dp(12).toFloat(); setColor(surface); setStroke(dp(1), line) }
        setPadding(dp(4), dp(4), dp(4), dp(4))
        for ((id, label) in listOf("home" to "Home", "device" to "My device", "settings" to "Settings")) {
            addView(TextView(this@MainActivity).apply {
                text = label; textSize = 14f; gravity = Gravity.CENTER; setPadding(0, dp(9), 0, dp(9))
                setTypeface(typeface, Typeface.BOLD)
                val on = tab == id
                setTextColor(if (on) Color.WHITE else muted)
                if (on) background = GradientDrawable().apply { cornerRadius = dp(9).toFloat(); setColor(green) }
                setOnClickListener { tab = id; benchJustDone = false; render() }
            }, LinearLayout.LayoutParams(0, -2, 1f))
        }
    }

    private fun renderPair() {
        val server = field("Server", prefs.server.ifEmpty { DEFAULT_SERVER })
        val code = field("6-digit code from the dashboard", number = true)
        val status = text("")
        root.addView(text("Earn by letting your phone serve AI jobs while it charges."))
        root.addView(card(text("Pair this phone", 17f, true),
            text("Easiest: on this phone open the dashboard, tap Get pairing code, then Pair this phone. Or type the code here:", 13f, color = Color.GRAY),
            server, code, status, button("Pair") {
                pairWith(server.text.toString().trim(), code.text.toString().trim()) { m -> ui.post { status.text = m } }
            }))
    }

    private fun phoneRamGb(): Double = (getSystemService(android.app.ActivityManager::class.java)).let {
        val mi = android.app.ActivityManager.MemoryInfo(); it.getMemoryInfo(mi); mi.totalMem / 1e9
    }

    private var catalog: List<CatalogModel> = emptyList()
    private var progress: android.widget.ProgressBar? = null

    private fun renderModel() {
        val ram = phoneRamGb()
        val status = text(setupStatus.ifEmpty { if (prefs.klass == "unrated") "Too slow with that model (below 3 tokens/s). Pick a smaller one." else "" })
        val picker = android.widget.Spinner(this)
        val go = button("Download and start") {}
        val bar = android.widget.ProgressBar(this, null, android.R.attr.progressBarStyleHorizontal).apply { max = 100; visibility = View.GONE }
        progress = bar
        root.addView(text("Pick the AI model this phone will run. We've chosen the best one for your %.0f GB of RAM.".format(ram)))
        root.addView(card(text("Model", 17f, true), picker, go, bar, status))

        fun fill(models: List<CatalogModel>) {
            catalog = models
            val labels = models.map { m ->
                val fits = m.phoneMinRamGb <= ram
                "${m.label} · ${m.ggufMb} MB" + if (fits) "" else " (needs ${m.phoneMinRamGb.toInt()} GB RAM)"
            }
            picker.adapter = android.widget.ArrayAdapter(this, android.R.layout.simple_spinner_dropdown_item, labels)
            Api.pickForPhone(models, ram)?.let { best -> picker.setSelection(models.indexOf(best)) }
        }
        if (catalog.isNotEmpty()) fill(catalog) else {
            status.text = "Loading models…"
            Thread {
                val models = runCatching { Api.catalog(prefs.server) }.getOrDefault(emptyList())
                ui.post {
                    status.text = if (models.isEmpty()) "Couldn't load the model list. Check your connection and reopen the app." else ""
                    fill(models)
                }
            }.start()
        }
        statusView = status
        if (setupBusy) { // a download is already running (e.g. the app was reopened): show it, don't start another
            go.isEnabled = false; go.text = "Working…"; status.text = setupStatus
            if (setupPct >= 0) { bar.visibility = View.VISIBLE; bar.progress = setupPct }
        }
        go.setOnClickListener {
            val m = catalog.getOrNull(picker.selectedItemPosition) ?: return@setOnClickListener
            if (setupBusy) return@setOnClickListener
            go.isEnabled = false
            go.text = "Working…"
            status.text = "Starting download of ${m.ggufMb} MB… you can rotate or lock the phone"
            runSetup { say -> setupModel(m.ggufUrl, m.id, m.paramsB.toFloat(), m.sha256, m.ggufMb.toLong(), say) }
        }

        // Advanced: any GGUF link (most testers never need this)
        val url = field("Model file URL (.gguf)")
        val id = field("Model name, e.g. qwen3-1.7b", prefs.modelId)
        val params = field("Size in billions, e.g. 1.7", number = true)
        val adv = card(text("Advanced: use your own model", 15f, true), url, id, params, button("Use this model", primary = false) {
            val pb = params.text.toString().toFloatOrNull() ?: 0f
            if (id.text.isBlank() || pb <= 0f || url.text.isBlank()) { status.text = "Enter the link, name and size."; return@button }
            if (setupBusy) return@button
            val u = url.text.toString().trim(); val mid = id.text.toString().trim()
            runSetup { say -> setupModel(u, mid, pb, say = say) }
        }).apply { visibility = View.GONE }
        val stored = File(filesDir, "models").listFiles()?.filter { it.isFile } ?: emptyList()
        if (stored.isNotEmpty() && !setupBusy) {
            val mb = stored.sumOf { it.length() } / 1_000_000
            root.addView(button("Remove downloaded models (%d MB)".format(mb), primary = false) {
                if (setupBusy) return@button
                NodeService.engine?.close(); NodeService.engine = null
                stored.forEach { it.delete() }
                prefs.modelPath = ""; prefs.klass = ""
                setupStatus = "Removed %d MB of models.".format(mb)
                render()
            })
        }
        root.addView(button("Advanced: use my own model file", primary = false) { adv.visibility = View.VISIBLE })
        root.addView(adv)
    }

    /** Runs a model setup on one background thread, holding the CPU and Wi-Fi awake, and reports
     *  progress to whichever activity is on screen (it survives rotation and reopening the app). */
    private fun runSetup(work: ((String) -> Unit) -> Unit) {
        setupBusy = true; setupStatus = ""; setupPct = -1
        window.addFlags(android.view.WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        val app = applicationContext
        Thread {
            val wake = (app.getSystemService(POWER_SERVICE) as android.os.PowerManager)
                .newWakeLock(android.os.PowerManager.PARTIAL_WAKE_LOCK, "infera:model-download").apply { acquire(3 * 60 * 60 * 1000L) }
            @Suppress("DEPRECATION")
            val wifi = (app.getSystemService(WIFI_SERVICE) as android.net.wifi.WifiManager)
                .createWifiLock(android.net.wifi.WifiManager.WIFI_MODE_FULL_HIGH_PERF, "infera:model-download").apply { acquire() }
            try {
                work { s -> setupStatus = s; ui.post { current?.statusView?.text = s } }
            } finally {
                runCatching { wake.release() }; runCatching { wifi.release() }
                setupBusy = false; setupPct = -1
                ui.post { current?.let { a -> a.window.clearFlags(android.view.WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON); a.render() } }
            }
        }.start()
    }

    private fun setupModel(url: String, modelId: String, paramsB: Float, sha256: String = "", sizeMb: Long = 0, say: (String) -> Unit) {
        try {
            val dir = File(filesDir, "models").apply { mkdirs() }
            val file = File(dir, "$modelId.gguf")
            if (url.isNotEmpty() && !file.exists()) {
                // Resumable: keep the partial file and ask only for the missing bytes (HTTP Range);
                // retry by itself when the connection drops.
                val tmp = File(dir, "$modelId.part")
                val shaFile = File(dir, "$modelId.sha256")
                // Disk quota: one model at a time. Remove other models (and their partial files) first.
                dir.listFiles()?.filter { !it.name.startsWith("$modelId.") }?.forEach { it.delete() }
                val need = (sizeMb * 1_000_000L - tmp.length()).coerceAtLeast(0) + 500_000_000L
                if (sizeMb > 0 && dir.usableSpace < need)
                    throw IllegalStateException("not enough storage: needs ${need / 1_000_000} MB free, ${dir.usableSpace / 1_000_000} MB available")
                if (sha256.isNotEmpty()) shaFile.writeText(sha256)
                var attempt = 0
                while (true) {
                    try {
                        downloadResumable(url, tmp, shaFile, say)
                        break
                    } catch (e: java.io.IOException) {
                        attempt++
                        if (attempt > 30) throw IllegalStateException("download keeps dropping (${e.message}); tap the button to continue from ${tmp.length() / 1_000_000} MB")
                        val wait = minOf(attempt * 3, 30)
                        say("Connection dropped at ${tmp.length() / 1_000_000} MB (${e.message}), continuing in $wait s…")
                        Thread.sleep(wait * 1000L)
                    }
                }
                // Verified download: SHA-256 from the catalog, or the host's X-Linked-Etag.
                val expect = if (shaFile.exists()) shaFile.readText().trim().lowercase() else ""
                if (expect.length == 64) {
                    say("Checking the file…")
                    val got = sha256Of(tmp)
                    if (got != expect) {
                        tmp.delete(); shaFile.delete()
                        throw IllegalStateException("the downloaded file is damaged (checksum mismatch); tap the button to download it again")
                    }
                }
                if (!tmp.renameTo(file)) throw IllegalStateException("couldn't save the model file")
                shaFile.delete()
            } else if (file.exists()) {
                say("Model already downloaded")
            }
            if (!file.exists()) throw IllegalStateException("enter a model URL to download")
            ui.post { current?.progress?.isIndeterminate = true }
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
            if (prefs.klass == "unrated") {
                // Too slow to earn with this model: free the space straight away.
                NodeService.engine?.close(); NodeService.engine = null
                val mb = file.length() / 1_000_000
                file.delete()
                prefs.modelPath = ""
                say("%.1f tokens/s: too slow for this model (needs 3+). Removed it to free %d MB. Pick a smaller one.".format(tps, mb))
                ui.post { current?.render() }
                return
            }
            say("%.1f tokens/s · class %s · %d threads".format(tps, prefs.klass, LlamaEngine.bestThreads()))
            prefs.benchAt = System.currentTimeMillis(); prefs.threads = LlamaEngine.bestThreads()
            if (prefs.klass != "unrated") { prefs.enabled = true; NodeService.start(this) }
            ui.post { current?.let { a -> a.tab = "device"; a.benchJustDone = true; a.render() } }
            ui.post { current?.progress?.visibility = View.GONE }
        } catch (e: Throwable) {
            ui.post { current?.progress?.visibility = View.GONE }
            say("Problem: ${e.message ?: e.javaClass.simpleName}. Check Wi-Fi and storage space, then tap the button again.")
        }
    }


    /** Downloads into [tmp], continuing from its current size when the server supports ranges. */
    private fun downloadResumable(url: String, tmp: File, shaFile: File, say: (String) -> Unit) {
        val have = if (tmp.exists()) tmp.length() else 0L
        val req = Request.Builder().url(url).apply { if (have > 0) header("Range", "bytes=$have-") }.build()
        downloadHttp.newCall(req).execute().use { r ->
            if (r.code >= 500 || r.code == 429) throw java.io.IOException("server busy (${r.code})")
            if (!r.isSuccessful) throw IllegalStateException("download failed (${r.code})")
            if (!shaFile.exists()) {
                // Hugging Face puts the file's SHA-256 in X-Linked-Etag on the redirect.
                var p: okhttp3.Response? = r
                while (p != null) {
                    val tag = (p.header("X-Linked-Etag") ?: "").trim('"', ' ').lowercase()
                    if (tag.length == 64 && tag.all { it in "0123456789abcdef" }) { shaFile.writeText(tag); break }
                    p = p.priorResponse
                }
            }
            val resumed = r.code == 206 && have > 0
            val start = if (resumed) have else 0L
            val total = (r.body!!.contentLength().takeIf { it > 0 } ?: -1L).let { if (it > 0) it + start else -1L }
            if (resumed) say("Continuing from ${start / 1_000_000} MB…")
            r.body!!.byteStream().use { input ->
                java.io.FileOutputStream(tmp, resumed).use { out ->
                    val buf = ByteArray(1 shl 16); var done = start; var last = start
                    while (true) {
                        val n = input.read(buf); if (n < 0) break
                        out.write(buf, 0, n); done += n
                        if (done - last > 2_000_000) {
                            last = done
                            val pct = if (total > 0) (done * 100 / total).toInt() else 0
                            setupPct = pct
                            ui.post { current?.progress?.let { b -> b.visibility = View.VISIBLE; b.isIndeterminate = total <= 0; b.progress = pct } }
                            say("Downloading… ${done / 1_000_000} MB" + if (total > 0) " of ${total / 1_000_000} MB ($pct%)" else "")
                        }
                    }
                }
            }
            if (total > 0 && tmp.length() < total) throw java.io.IOException("stream ended early")
        }
    }

    /** Hash of ANDROID_ID (stable across reinstalls of this app on this phone). */
    private fun hardwareId(): String {
        val raw = android.provider.Settings.Secure.getString(contentResolver, android.provider.Settings.Secure.ANDROID_ID) ?: return ""
        val md = java.security.MessageDigest.getInstance("SHA-256")
        return md.digest("infera|$raw|${Build.MANUFACTURER}|${Build.MODEL}".toByteArray()).joinToString("") { "%02x".format(it) }
    }

    private fun sha256Of(f: File): String {
        val md = java.security.MessageDigest.getInstance("SHA-256")
        f.inputStream().use { i -> val b = ByteArray(1 shl 20); while (true) { val n = i.read(b); if (n < 0) break; md.update(b, 0, n) } }
        return md.digest().joinToString("") { "%02x".format(it) }
    }

    private fun editLimits() {
        fun f(v: Float) = if (v > 0) v.toString() else ""
        val pay = field("Minimum pay per job, USD (e.g. 0.001)", f(prefs.minPayUsd), number = true)
        val hour = field("Minimum pay per hour, USD (e.g. 0.20)", f(prefs.minPerHourUsd), number = true)
        val mins = field("Longest job, minutes (e.g. 5)", f(prefs.maxMinutes), number = true)
        val box = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(dp(20), dp(8), dp(20), 0); addView(pay); addView(hour); addView(mins)
            addView(text("Leave empty for no limit.", 12f, color = Color.GRAY)) }
        android.app.AlertDialog.Builder(this).setTitle("Auto-approve limits").setView(box)
            .setPositiveButton("Save") { _, _ ->
                prefs.minPayUsd = pay.text.toString().toFloatOrNull() ?: 0f
                prefs.minPerHourUsd = hour.text.toString().toFloatOrNull() ?: 0f
                prefs.maxMinutes = mins.text.toString().toFloatOrNull() ?: 0f
                NodeService.node?.limits = prefs.limits
                render()
            }.setNegativeButton("Cancel", null).show()
    }

    private fun refreshRemote() {
        if (rToday.isNotEmpty() && System.currentTimeMillis() / 1000 % 15 != 0L) return
        Thread {
            val v = Api.deviceMe(prefs.pairing) ?: return@Thread
            val today = v["today"]?.jsonObject
            val bal = v["balance"]?.jsonObject
            val dev = v["device"]?.jsonObject
            fun usd(m: Long?) = if (m == null) "–" else "$" + "%.${if (m != 0L && kotlin.math.abs(m) < 10_000) 6 else 2}f".format(m / 1e6)
            rToday = usd(today?.get("earned_today")?.jsonPrimitive?.longOrNull)
            rJobs = today?.get("jobs_today")?.jsonPrimitive?.content ?: "0"
            rAvail = usd(bal?.get("available")?.jsonPrimitive?.longOrNull)
            rHeld = usd(bal?.get("held")?.jsonPrimitive?.longOrNull)
            rDep = "%.0f / 100".format(dev?.get("dependability")?.jsonPrimitive?.doubleOrNull ?: 0.0)
        }.start()
    }

    // ---------- Home: what's happening now ----------
    private fun renderHome() {
        val n = NodeService.node
        val s = n?.status
        val running = prefs.enabled && n != null
        val checks = PhoneRules(this, prefs).checks()
        val failing = checks.filter { !it.ok }
        val (headline, color, explain) = when {
            !running -> Triple("Not running", muted, "Tap Start earning to let this phone take jobs.")
            s?.runningJob != null -> Triple("Working on a job", amber, "Earning about $%.4f for this job. Keep the phone plugged in.".format(s.runningPayUsd))
            s?.online == false -> Triple("Offline", muted, "You turned jobs off. Tap Go online to earn again.")
            s?.connected != true -> Triple("Connecting…", muted, "Reaching the Infera network.")
            s.status == "idle" -> Triple("Online · ready for jobs", green, "Everything is set. Jobs arrive when customers send them; you'll see each one here.")
            failing.isNotEmpty() -> Triple("Waiting to start", amber, "This phone takes jobs only when it's safe to. Waiting for: " + failing.joinToString(", ") { it.label.lowercase() } + ".")
            else -> Triple("Paused", amber, s.pauseReason.replaceFirstChar { it.uppercase() })
        }
        root.addView(card(
            LinearLayout(this).apply {
                orientation = LinearLayout.HORIZONTAL; gravity = Gravity.CENTER_VERTICAL
                addView(View(this@MainActivity).apply { background = GradientDrawable().apply { shape = GradientDrawable.OVAL; setColor(color) } },
                    LinearLayout.LayoutParams(dp(14), dp(14)).apply { rightMargin = dp(10) })
                addView(text(headline, 20f, true, color))
            },
            text(explain, 14f, color = muted),
            if (!running) button("Start earning") { prefs.enabled = true; NodeService.start(this); ui.postDelayed({ render() }, 800) }
            else button(if (s?.online == false) "Go online" else "Go offline", primary = s?.online == false) { n.setOnline(s?.online == false) },
        ))

        s?.offer?.let { o ->
            root.addView(card(
                text("NEW JOB OFFER · ${o.secondsLeft()}s left", 12f, true, muted),
                text("$%.4f".format(o.estPayUsd), 34f, true, amber),
                text("≈ $%.2f per hour · ${o.jobType.replace('_', ' ')} · about ${o.estSeconds.toInt().coerceAtLeast(1)} s".format(o.perHourUsd), 14f),
                button("Accept") { n?.decide(true) },
                button("Decline", primary = false) { n?.decide(false) },
                text("Declining is free. Once accepted, the job must finish.", 12f, color = muted),
            ).apply { background = GradientDrawable().apply { cornerRadius = dp(14).toFloat(); setColor(surface); setStroke(dp(2), green) } })
        }
        if (s?.runningJob != null) {
            root.addView(card(
                text("Running job · private and sealed", 15f, true),
                text("You can't see what it is, and neither can anyone else on this phone.", 12f, color = muted),
                button("Finish this job, then go offline", primary = false) { n?.finishThenOffline() },
                button(if (stopArmed) "Tap again: stop now and take the penalty" else "Stop now", primary = false) {
                    if (stopArmed) { n?.stopNow(); stopArmed = false } else stopArmed = true
                },
                text("Stopping now loses this job's ~$%.4f, debits $%.4f (2×) and costs 10 dependability points.".format(s.runningPayUsd, s.runningPayUsd * 2), 12f, color = muted),
            ))
        }

        root.addView(heading("Ready to earn when"))
        root.addView(card(*checks.map { checkRow(it.ok, it.label, it.detail) }.toTypedArray(),
            checkRow(s?.connected == true, "Connected to Infera", if (s?.connected == true) "Online" else "Connecting…")))

        root.addView(heading("Earnings"))
        root.addView(card(
            row("Today", if (rToday.isEmpty()) "…" else "$rToday · $rJobs jobs", green),
            row("Available to withdraw", rAvail.ifEmpty { "…" }),
            row("On hold (safety period)", rHeld.ifEmpty { "…" }),
            row("Dependability", rDep.ifEmpty { "…" }),
            text("Withdraw in USDT from the dashboard. Earnings are held a few days first; reliable devices are paid sooner.", 12f, color = muted),
        ))

        root.addView(heading("Activity"))
        val ev = (s?.events ?: emptyList()).take(10)
        root.addView(card(text(if (ev.isEmpty()) "Nothing yet. Jobs and status changes show up here." else ev.joinToString("\n"), 12f, color = muted).apply { gravity = Gravity.START }))
    }

    // ---------- My device: benchmark summary and potential ----------
    private fun renderDevice() {
        val tps = prefs.tps.toDouble()
        val m1 = prefs.klass == "M1"
        if (benchJustDone) root.addView(card(
            text("Benchmark complete ✓", 18f, true, green),
            text("Here's what this phone can do on Infera. It has started earning; you'll find this page under My device any time.", 14f, color = muted),
            button("Go to Home") { tab = "home"; benchJustDone = false; render() },
        ))
        root.addView(heading("Result"))
        root.addView(card(
            text(if (m1) "Class M1 · Mobile Plus" else "Class M2 · Mobile Lite", 20f, true, green),
            text(if (m1) "Fast enough for live chat replies, where a customer is waiting for the answer."
                 else "Good for batch jobs, where nobody is waiting, so speed matters less.", 14f, color = muted),
            row("Speed", "%.1f tokens/s".format(tps)),
            row("Model", "${prefs.modelId} (%.1f B)".format(prefs.paramsB)),
            row("Memory", "%.0f GB RAM".format(phoneRamGb())),
            row("Processor", (if (Build.VERSION.SDK_INT >= 31) Build.SOC_MODEL else Build.HARDWARE) + " · ${prefs.threads.takeIf { it > 0 } ?: LlamaEngine.bestThreads()} fast cores"),
            row("Tested", if (prefs.benchAt > 0) java.text.DateFormat.getDateTimeInstance(java.text.DateFormat.MEDIUM, java.text.DateFormat.SHORT).format(java.util.Date(prefs.benchAt)) else "–"),
        ))
        root.addView(heading("Jobs this phone can take"))
        val can = if (m1) listOf("Live chat replies" to "Customers waiting on an answer. Paid the most.", "Batch chat" to "Large jobs that can wait; run while you sleep.")
                  else listOf("Batch chat" to "Jobs where nobody is waiting; run overnight on the charger.")
        val speechOn = prefs.speechPath.isNotEmpty() && File(prefs.speechPath).exists()
        val rows = can.map { checkRow(true, it.first, it.second) } +
            checkRow(speechOn, "Transcription (speech to text)", if (speechOn) "On: Whisper is ready. Pays per audio minute, the best earner per hour."
                                                                else "Off: turn it on below. Pays per audio minute, the best earner per hour.") +
            checkRow(false, "Embeddings", "Turning text into search vectors. Coming to phones soon.")
        root.addView(card(*rows.toTypedArray(),
            if (speechOn) View(this) else button(if (setupBusy) "Working…" else "Turn on transcription (60 MB download)") { enableSpeech() }))

        // Live prices from the server. A typical job has ~2 prompt tokens per answer token.
        val pr = pricing
        if (System.currentTimeMillis() / 1000 % 30 == 0L || pr == Pricing()) Thread { Api.pricing(prefs.server)?.let { pricing = it } }.start()
        val perHour = tps * 3600 / 1e6 * (pr.outPerM + 2 * pr.inPerM) * pr.ownerShare
        root.addView(heading("Earning potential"))
        root.addView(card(
            row("Per hour of chat jobs", "≈ $%.4f".format(perHour), green),
            row("Quiet month (2 h of jobs a night)", "≈ $%.2f".format(perHour * 2 * 30)),
            row("Busy month (6 h of jobs a night)", "≈ $%.2f".format(perHour * 6 * 30), green),
            text("At today's prices: $%.2f per million answer tokens, $%.2f per million prompt tokens, and you keep %.0f%%. Coming soon to phones, and paying more per hour: transcription (speech to text) and running several jobs at once. Real earnings depend on demand and your dependability.".format(pr.outPerM, pr.inPerM, pr.ownerShare * 100), 12f, color = muted),
        ))
        root.addView(heading("Improve your result"))
        root.addView(card(
            text("• Keep it plugged in overnight on Wi-Fi: that's when most jobs run.\n• Don't stop accepted jobs: dependability raises your pay and gets you paid sooner.\n• Try a bigger model if this one is fast, or a smaller one if it's slow.", 14f),
            button("Run the benchmark again", primary = false) { rebenchmark() },
            button("Change model", primary = false) { stopNode(); prefs.enabled = false; prefs.modelPath = ""; prefs.klass = ""; setupStatus = ""; render() },
        ))
    }

    /** Downloads the Whisper model, checks it loads, then restarts the node so it offers transcription. */
    private fun enableSpeech() {
        if (setupBusy) return
        tab = "device"
        runSetup { say ->
            try {
                val m = Api.speechModel(prefs.server) ?: throw IllegalStateException("couldn't get the speech model list; check your connection")
                val dir = File(filesDir, "speech").apply { mkdirs() }
                val file = File(dir, "${m.id}.bin")
                if (!file.exists()) {
                    val tmp = File(dir, "${m.id}.part"); val sha = File(dir, "${m.id}.sha256")
                    if (m.sha256.isNotEmpty()) sha.writeText(m.sha256)
                    var attempt = 0
                    while (true) {
                        try { downloadResumable(m.ggufUrl, tmp, sha, say); break }
                        catch (e: java.io.IOException) {
                            if (++attempt > 30) throw IllegalStateException("download keeps dropping; tap the button to continue")
                            say("Connection dropped, continuing in ${minOf(attempt * 3, 30)} s…"); Thread.sleep(minOf(attempt * 3, 30) * 1000L)
                        }
                    }
                    val expect = if (sha.exists()) sha.readText().trim().lowercase() else ""
                    if (expect.length == 64 && sha256Of(tmp) != expect) { tmp.delete(); sha.delete(); throw IllegalStateException("the download was damaged; tap the button to try again") }
                    tmp.renameTo(file); sha.delete()
                }
                say("Checking the speech model…")
                NodeService.speech?.close(); NodeService.speech = null
                WhisperEngine(file.path, m.id, cacheDir).close() // loads it once to be sure it works
                prefs.speechPath = file.path; prefs.speechId = m.id
                say("Transcription is on.")
                if (prefs.enabled) // reconnect so the network knows this phone now takes transcription jobs
                    startService(android.content.Intent(this, NodeService::class.java).setAction(NodeService.ACTION_RELOAD))
            } catch (e: Throwable) {
                say("Problem: ${e.message ?: e.javaClass.simpleName}")
            }
        }
    }

    private fun stopNode() {
        startService(android.content.Intent(this, NodeService::class.java).setAction(NodeService.ACTION_STOP))
    }

    private fun rebenchmark() {
        if (setupBusy) return
        val path = prefs.modelPath; val id = prefs.modelId; val pb = prefs.paramsB
        stopNode(); prefs.enabled = false
        prefs.klass = ""; prefs.modelPath = path
        tab = "device"; render()
        runSetup { say -> setupModel("", id, pb, say = say) }
    }

    // ---------- Settings ----------
    private fun renderSettings() {
        val n = NodeService.node
        val running = prefs.enabled && n != null
        val ask = Switch(this).apply { text = "Ask before every job"; setTextColor(fg); isChecked = prefs.mode == "ask"
            setOnCheckedChangeListener { _, on -> prefs.mode = if (on) "ask" else "auto"; n?.setMode(prefs.mode) } }
        val screen = Switch(this).apply { text = "Charge Mode: also work with the screen on"; setTextColor(fg); isChecked = prefs.workWithScreenOn
            setOnCheckedChangeListener { _, on -> prefs.workWithScreenOn = on } }
        val lim = prefs.limits
        val limText = if (lim == AutoLimits()) "Every job is accepted automatically." else listOfNotNull(
            if (lim.minPayUsd > 0) "min $%.4f a job".format(lim.minPayUsd) else null,
            if (lim.minPerHourUsd > 0) "min $%.2f an hour".format(lim.minPerHourUsd) else null,
            if (lim.maxMinutes > 0) "max %.0f min".format(lim.maxMinutes) else null,
        ).joinToString(" · ", "Auto-accept only: ") + ". Others come as offers."
        root.addView(heading("Jobs"))
        root.addView(card(ask, text("Off: jobs are accepted automatically. On: each job shows an offer with the pay; you accept or decline.", 12f, color = muted),
            screen, text("By default jobs run only while the phone is locked, so it never slows you down.", 12f, color = muted),
            text(limText, 13f), button("Auto-accept limits…", primary = false) { editLimits() }))
        root.addView(heading("Model"))
        root.addView(card(row("Model", prefs.modelId), row("Speed", "%.1f tokens/s".format(prefs.tps)),
            button("Change model", primary = false) { stopNode(); prefs.enabled = false; prefs.modelPath = ""; prefs.klass = ""; setupStatus = ""; render() }))
        root.addView(heading("Account"))
        root.addView(card(row("Server", prefs.server.removePrefix("https://")), row("Device", prefs.deviceId),
            if (running && n?.status?.runningJob == null) button("Stop the Infera service", primary = false) {
                startService(android.content.Intent(this, NodeService::class.java).setAction(NodeService.ACTION_STOP))
                prefs.enabled = false; ui.postDelayed({ render() }, 800)
            } else View(this),
            button("Pair again / use another account", primary = false) {
                startService(android.content.Intent(this, NodeService::class.java).setAction(NodeService.ACTION_STOP))
                prefs.enabled = false; prefs.deviceId = ""; prefs.token = ""
                render()
            }))
    }
}
