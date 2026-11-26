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

    /**
     * Every condition for taking jobs, for the app's checklist. By default phones work on
     * battery (at least 40%, or 20% while charging) and while the screen is on; owners can
     * require charging or a locked screen in Settings.
     */
    fun checks(): List<Check> {
        val batt = ctx.registerReceiver(null, IntentFilter(Intent.ACTION_BATTERY_CHANGED))
        val plugged = (batt?.getIntExtra(BatteryManager.EXTRA_PLUGGED, 0) ?: 0) != 0
        val level = batt?.let { it.getIntExtra(BatteryManager.EXTRA_LEVEL, 0) * 100 / it.getIntExtra(BatteryManager.EXTRA_SCALE, 100).coerceAtLeast(1) } ?: 0
        val minLevel = if (plugged) 20 else 40
        val cm = ctx.getSystemService(ConnectivityManager::class.java)
        val caps = cm.getNetworkCapabilities(cm.activeNetwork)
        val wifi = caps != null && caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED)
        val pm = ctx.getSystemService(PowerManager::class.java)
        val cool = coolEnough(pm)
        val list = mutableListOf<Check>()
        if (prefs.onlyCharging) list += Check("Charging", plugged, if (plugged) "Plugged in" else "You chose: only while charging")
        list += Check("Battery at least $minLevel%", level >= minLevel,
            "$level%" + if (plugged) " · charging" else " · on battery (20% is enough while charging)")
        list += Check("On Wi-Fi", wifi, if (wifi) "Connected" else "Jobs never use mobile data")
        list += Check("Phone is cool", cool, if (cool) "Normal temperature" else "Resting until it cools down")
        if (prefs.onlyLocked) {
            val locked = !pm.isInteractive
            list += Check("Screen off", locked, if (locked) "Screen is off" else "You chose: only while the phone is locked")
        }
        return list
    }

    /** null when new jobs may start, otherwise the first reason they can't. */
    fun check(): String? = checks().firstOrNull { !it.ok }?.let { c ->
        when {
            c.label == "Charging" -> "not charging"
            c.label.startsWith("Battery") -> "battery low (${c.detail.substringBefore(' ')})"
            c.label == "On Wi-Fi" -> "not on Wi-Fi"
            c.label == "Phone is cool" -> "phone is warm"
            else -> "screen is on"
        }
    }

    /**
     * Not hot now, and not about to throttle: Android's forecast of thermal headroom in
     * 10 seconds (1.0 = throttling starts). Pausing new jobs early keeps speed steady instead
     * of the phone slowing itself down mid-job.
     */
    private fun coolEnough(pm: PowerManager): Boolean {
        if (Build.VERSION.SDK_INT >= 29 && pm.currentThermalStatus >= PowerManager.THERMAL_STATUS_MODERATE) return false
        if (Build.VERSION.SDK_INT >= 30) {
            val h = pm.getThermalHeadroom(10)
            if (!h.isNaN() && h >= 0.9f) return false
        }
        return true
    }
}
