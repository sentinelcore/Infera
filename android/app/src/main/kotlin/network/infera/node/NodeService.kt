package network.infera.node

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import android.os.PowerManager
import java.util.concurrent.CopyOnWriteArrayList

/** Foreground service: keeps the node connected while the owner lets it run. */
class NodeService : Service() {
    companion object {
        const val ACTION_ACCEPT = "network.infera.node.ACCEPT"
        const val ACTION_DECLINE = "network.infera.node.DECLINE"
        const val ACTION_STOP = "network.infera.node.STOP"
        /** Reconnect with the current settings and models (e.g. after turning on transcription). */
        const val ACTION_RELOAD = "network.infera.node.RELOAD"
        @Volatile var node: Node? = null
        @Volatile var engine: LlamaEngine? = null
        @Volatile var speech: WhisperEngine? = null
        val listeners = CopyOnWriteArrayList<(NodeStatus) -> Unit>()

        fun start(ctx: Context) {
            val i = Intent(ctx, NodeService::class.java)
            if (Build.VERSION.SDK_INT >= 26) ctx.startForegroundService(i) else ctx.startService(i)
        }
    }

    private lateinit var prefs: Prefs
    private var wake: PowerManager.WakeLock? = null
    private var lastOffer = ""

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        prefs = Prefs(this)
        val nm = getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(NotificationChannel("node", "Infera node", NotificationManager.IMPORTANCE_LOW))
        nm.createNotificationChannel(NotificationChannel("offers", "Job offers", NotificationManager.IMPORTANCE_HIGH))
        val n = ongoing("Starting…")
        if (Build.VERSION.SDK_INT >= 29) startForeground(1, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC) else startForeground(1, n)
        wake = getSystemService(PowerManager::class.java).newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "infera:node")
        Thread { boot() }.start()
    }

    private fun boot() {
        try {
            val eng = engine ?: LlamaEngine(prefs.modelPath, prefs.modelId).also { engine = it }
            val ram = (getSystemService(android.app.ActivityManager::class.java)).let {
                val mi = android.app.ActivityManager.MemoryInfo(); it.getMemoryInfo(mi); mi.totalMem / 1e9
            }
            val rules = PhoneRules(this, prefs)
            val nd = Node(prefs.pairing, prefs.identity, eng, Bench(prefs.tps.toDouble(), prefs.paramsB.toDouble(), ram, prefs.klass),
                "android ${Build.VERSION.RELEASE} ${Build.SUPPORTED_ABIS.firstOrNull() ?: ""} ${Build.MODEL}", { rules.check() }, ::onStatus)
            nd.setMode(prefs.mode)
            nd.limits = prefs.limits
            if (prefs.speechPath.isNotEmpty() && java.io.File(prefs.speechPath).exists()) {
                runCatching { speech ?: WhisperEngine(prefs.speechPath, prefs.speechId, cacheDir).also { speech = it } }
                    .onSuccess { nd.speech = it }
            }
            node = nd
            nd.start()
        } catch (e: Throwable) {
            update(ongoing("Couldn't start: ${e.message}"))
        }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_ACCEPT -> node?.decide(true)
            ACTION_DECLINE -> node?.decide(false)
            ACTION_RELOAD -> Thread {
                // Wait for a running job to finish, then reconnect with the new model list.
                repeat(600) { if (node?.status?.runningJob == null) return@repeat; Thread.sleep(1000) }
                node?.stop(); node = null
                boot()
            }.start()
            ACTION_STOP -> {
                prefs.enabled = false
                node?.finishThenOffline()
                if (node?.status?.runningJob == null) stopSelf()
            }
        }
        return START_STICKY
    }

    override fun onDestroy() {
        node?.stop(); node = null
        if (wake?.isHeld == true) wake?.release()
        super.onDestroy()
    }

    private fun onStatus(s: NodeStatus) {
        // Hold a wake lock only while a job runs.
        if (s.runningJob != null && wake?.isHeld == false) wake?.acquire(15 * 60 * 1000L)
        if (s.runningJob == null && wake?.isHeld == true) wake?.release()
        val text = when {
            s.runningJob != null -> "Working · earns ~$%.4f".format(s.runningPayUsd)
            !s.connected -> "Connecting…"
            s.status == "idle" -> "Online · waiting for jobs"
            else -> "Paused · ${s.pauseReason}"
        }
        update(ongoing(text))
        val o = s.offer
        if (o != null && o.jobId != lastOffer) {
            lastOffer = o.jobId
            offerNotification(o)
        }
        if (o == null && lastOffer.isNotEmpty()) getSystemService(NotificationManager::class.java).cancel(2)
        listeners.forEach { it(s) }
    }

    private fun pi(action: String, code: Int) =
        PendingIntent.getService(this, code, Intent(this, NodeService::class.java).setAction(action), PendingIntent.FLAG_IMMUTABLE)

    private fun openApp() = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)

    private fun ongoing(text: String): Notification = Notification.Builder(this, "node")
        .setSmallIcon(android.R.drawable.stat_sys_upload).setContentTitle("Infera").setContentText(text)
        .setOngoing(true).setContentIntent(openApp())
        .addAction(Notification.Action.Builder(null, "Finish & stop", pi(ACTION_STOP, 3)).build())
        .build()

    private fun offerNotification(o: Offer) {
        val n = Notification.Builder(this, "offers")
            .setSmallIcon(android.R.drawable.stat_notify_more)
            .setContentTitle("New job · $%.4f".format(o.estPayUsd))
            .setContentText("${o.jobType.replace('_', ' ')} · about ${o.estSeconds.toInt().coerceAtLeast(1)} s · ≈ $%.2f/hour".format(o.perHourUsd))
            .setTimeoutAfter(o.ttlSeconds * 1000L).setAutoCancel(true).setContentIntent(openApp())
            .addAction(Notification.Action.Builder(null, "Accept", pi(ACTION_ACCEPT, 1)).build())
            .addAction(Notification.Action.Builder(null, "Decline", pi(ACTION_DECLINE, 2)).build())
            .build()
        getSystemService(NotificationManager::class.java).notify(2, n)
    }

    private fun update(n: Notification) = getSystemService(NotificationManager::class.java).notify(1, n)
}

class BootReceiver : android.content.BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        if (intent.action == Intent.ACTION_BOOT_COMPLETED && Prefs(ctx).let { it.enabled && it.ready }) NodeService.start(ctx)
    }
}
