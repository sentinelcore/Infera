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
    /** Owner's choice: take jobs only while charging (default: battery is fine above 40%). */
    var onlyCharging: Boolean get() = p.getBoolean("only_charging", false); set(v) = p.edit().putBoolean("only_charging", v).apply()
    /** Owner's choice: take jobs only while the phone is locked (default: also while in use). */
    var onlyLocked: Boolean get() = p.getBoolean("only_locked", false); set(v) = p.edit().putBoolean("only_locked", v).apply()

    var minPayUsd: Float get() = p.getFloat("min_pay", 0f); set(v) = p.edit().putFloat("min_pay", v).apply()
    var minPerHourUsd: Float get() = p.getFloat("min_per_hour", 0f); set(v) = p.edit().putFloat("min_per_hour", v).apply()
    var maxMinutes: Float get() = p.getFloat("max_minutes", 0f); set(v) = p.edit().putFloat("max_minutes", v).apply()
    val limits: AutoLimits get() = AutoLimits(minPayUsd.toDouble(), minPerHourUsd.toDouble(), maxMinutes.toDouble())

    var speechPath: String get() = p.getString("speech_path", "") ?: ""; set(v) = p.edit().putString("speech_path", v).apply()
    var speechId: String get() = p.getString("speech_id", "") ?: ""; set(v) = p.edit().putString("speech_id", v).apply()
    var batchTps: Float get() = p.getFloat("batch_tps", 0f); set(v) = p.edit().putFloat("batch_tps", v).apply()
    var parallel: Int get() = p.getInt("parallel", 1); set(v) = p.edit().putInt("parallel", v).apply()
    var benchAt: Long get() = p.getLong("bench_at", 0); set(v) = p.edit().putLong("bench_at", v).apply()
    var threads: Int get() = p.getInt("threads", 0); set(v) = p.edit().putInt("threads", v).apply()

    val paired: Boolean get() = deviceId.isNotEmpty() && token.isNotEmpty()
    val ready: Boolean get() = paired && modelPath.isNotEmpty() && klass.isNotEmpty() && klass != "unrated"
    val pairing: Pairing get() = Pairing(server, deviceId, token)

    /**
     * Device identity (Ed25519 seed that signs every receipt). Stored encrypted with an AES key
     * that lives in the Android Keystore (hardware-backed where available), so the seed never
     * sits in plain text in the app's files.
     */
    val identity: Identity
        get() {
            p.getString("identity_enc", null)?.let { enc ->
                runCatching { return Identity(KeystoreBox.open(Base64.decode(enc, Base64.NO_WRAP))) }
            }
            // Migrate a plain seed from earlier builds, or create a new identity.
            val plain = p.getString("identity", null)
            val id = if (plain != null) Identity(Base64.decode(plain, Base64.NO_WRAP)) else Identity.generate()
            val sealed = runCatching { KeystoreBox.seal(id.seed) }.getOrNull()
            if (sealed != null) {
                p.edit().putString("identity_enc", Base64.encodeToString(sealed, Base64.NO_WRAP)).remove("identity").apply()
            } else if (plain == null) {
                p.edit().putString("identity", Base64.encodeToString(id.seed, Base64.NO_WRAP)).apply() // no Keystore: app-private file
            }
            return id
        }
}

/** AES-GCM with a non-exportable key in the Android Keystore. */
object KeystoreBox {
    private const val ALIAS = "infera_identity"

    private fun key(): javax.crypto.SecretKey {
        val ks = java.security.KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (ks.getKey(ALIAS, null) as? javax.crypto.SecretKey)?.let { return it }
        val gen = javax.crypto.KeyGenerator.getInstance(android.security.keystore.KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        gen.init(android.security.keystore.KeyGenParameterSpec.Builder(ALIAS,
            android.security.keystore.KeyProperties.PURPOSE_ENCRYPT or android.security.keystore.KeyProperties.PURPOSE_DECRYPT)
            .setBlockModes(android.security.keystore.KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(android.security.keystore.KeyProperties.ENCRYPTION_PADDING_NONE)
            .setKeySize(256).build())
        return gen.generateKey()
    }

    fun seal(plain: ByteArray): ByteArray {
        val c = javax.crypto.Cipher.getInstance("AES/GCM/NoPadding")
        c.init(javax.crypto.Cipher.ENCRYPT_MODE, key())
        return c.iv + c.doFinal(plain) // 12-byte IV, then ciphertext + tag
    }

    fun open(box: ByteArray): ByteArray {
        val c = javax.crypto.Cipher.getInstance("AES/GCM/NoPadding")
        c.init(javax.crypto.Cipher.DECRYPT_MODE, key(), javax.crypto.spec.GCMParameterSpec(128, box.copyOfRange(0, 12)))
        return c.doFinal(box, 12, box.size - 12)
    }
}
