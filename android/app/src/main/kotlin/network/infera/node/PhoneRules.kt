package network.infera.node

import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.os.BatteryManager
import android.os.Build
import android.os.PowerManager

/** Phone idle rules (ARCHITECTURE.md §3): returns null when new jobs may start, else the reason. */
class PhoneRules(private val ctx: Context, private val prefs: Prefs) {
    data class Check(val label: String, val ok: Boolean, val detail: String)

    /** Every condition for taking jobs, for the app's checklist. */
    fun checks(): List<Check> {
        val batt = ctx.registerReceiver(null, IntentFilter(Intent.ACTION_BATTERY_CHANGED))
        val plugged = (batt?.getIntExtra(BatteryManager.EXTRA_PLUGGED, 0) ?: 0) != 0
        val level = batt?.let { it.getIntExtra(BatteryManager.EXTRA_LEVEL, 0) * 100 / it.getIntExtra(BatteryManager.EXTRA_SCALE, 100).coerceAtLeast(1) } ?: 0
        val cm = ctx.getSystemService(ConnectivityManager::class.java)
        val caps = cm.getNetworkCapabilities(cm.activeNetwork)
        val wifi = caps != null && caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED)
        val pm = ctx.getSystemService(PowerManager::class.java)
        val cool = Build.VERSION.SDK_INT < 29 || pm.currentThermalStatus < PowerManager.THERMAL_STATUS_MODERATE
        val screenOk = !pm.isInteractive || prefs.workWithScreenOn
        return listOf(
            Check("Charging", plugged, if (plugged) "Plugged in" else "Plug in your charger"),
            Check("Battery above 50%", level >= 50, "$level%"),
            Check("On Wi-Fi", wifi, if (wifi) "Connected" else "Jobs never use mobile data"),
            Check("Phone is cool", cool, if (cool) "Normal temperature" else "Resting until it cools down"),
            Check("Screen off", screenOk, if (prefs.workWithScreenOn) "Charge Mode: works with the screen on" else if (screenOk) "Screen is off" else "Jobs start when you lock the phone"),
        )
    }

    fun check(): String? {
        val batt = ctx.registerReceiver(null, IntentFilter(Intent.ACTION_BATTERY_CHANGED))
        val plugged = (batt?.getIntExtra(BatteryManager.EXTRA_PLUGGED, 0) ?: 0) != 0
        val level = batt?.let { it.getIntExtra(BatteryManager.EXTRA_LEVEL, 0) * 100 / it.getIntExtra(BatteryManager.EXTRA_SCALE, 100).coerceAtLeast(1) } ?: 0
        if (!plugged) return "not charging"
        if (level < 50) return "battery below 50%"
        val cm = ctx.getSystemService(ConnectivityManager::class.java)
        val caps = cm.getNetworkCapabilities(cm.activeNetwork)
        if (caps == null || !caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED)) return "not on Wi-Fi"
        val pm = ctx.getSystemService(PowerManager::class.java)
        if (Build.VERSION.SDK_INT >= 29 && pm.currentThermalStatus >= PowerManager.THERMAL_STATUS_MODERATE) return "phone is warm"
        if (pm.isInteractive && !prefs.workWithScreenOn) return "screen is on"
        return null
    }
}
