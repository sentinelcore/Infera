package network.infera.node

import android.content.Context
import android.util.Base64

/** Saved settings: pairing, identity, model and benchmark. */
class Prefs(ctx: Context) {
    private val p = ctx.getSharedPreferences("infera", Context.MODE_PRIVATE)

    var server: String get() = p.getString("server", "") ?: ""; set(v) = p.edit().putString("server", v).apply()
    var deviceId: String get() = p.getString("device_id", "") ?: ""; set(v) = p.edit().putString("device_id", v).apply()
    var token: String get() = p.getString("token", "") ?: ""; set(v) = p.edit().putString("token", v).apply()
    var modelPath: String get() = p.getString("model_path", "") ?: ""; set(v) = p.edit().putString("model_path", v).apply()
    var modelId: String get() = p.getString("model_id", "") ?: ""; set(v) = p.edit().putString("model_id", v).apply()
    var paramsB: Float get() = p.getFloat("params_b", 0f); set(v) = p.edit().putFloat("params_b", v).apply()
    var tps: Float get() = p.getFloat("tps", 0f); set(v) = p.edit().putFloat("tps", v).apply()
    var klass: String get() = p.getString("class", "") ?: ""; set(v) = p.edit().putString("class", v).apply()
    var mode: String get() = p.getString("mode", "auto") ?: "auto"; set(v) = p.edit().putString("mode", v).apply()
    var enabled: Boolean get() = p.getBoolean("enabled", false); set(v) = p.edit().putBoolean("enabled", v).apply()
    var workWithScreenOn: Boolean get() = p.getBoolean("screen_on_ok", false); set(v) = p.edit().putBoolean("screen_on_ok", v).apply()

    var minPayUsd: Float get() = p.getFloat("min_pay", 0f); set(v) = p.edit().putFloat("min_pay", v).apply()
    var minPerHourUsd: Float get() = p.getFloat("min_per_hour", 0f); set(v) = p.edit().putFloat("min_per_hour", v).apply()
    var maxMinutes: Float get() = p.getFloat("max_minutes", 0f); set(v) = p.edit().putFloat("max_minutes", v).apply()
    val limits: AutoLimits get() = AutoLimits(minPayUsd.toDouble(), minPerHourUsd.toDouble(), maxMinutes.toDouble())

    val paired: Boolean get() = deviceId.isNotEmpty() && token.isNotEmpty()
    val ready: Boolean get() = paired && modelPath.isNotEmpty() && klass.isNotEmpty() && klass != "unrated"
    val pairing: Pairing get() = Pairing(server, deviceId, token)

    val identity: Identity
        get() {
            val s = p.getString("identity", null)
            if (s != null) return Identity(Base64.decode(s, Base64.NO_WRAP))
            val id = Identity.generate()
            p.edit().putString("identity", Base64.encodeToString(id.seed, Base64.NO_WRAP)).apply()
            return id
        }
}
