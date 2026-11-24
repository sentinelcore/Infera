package network.infera.node

import org.bouncycastle.crypto.digests.SHA256Digest
import org.bouncycastle.crypto.engines.ChaCha7539Engine
import org.bouncycastle.crypto.generators.HKDFBytesGenerator
import org.bouncycastle.crypto.modes.ChaCha20Poly1305
import org.bouncycastle.crypto.params.AEADParameters
import org.bouncycastle.crypto.params.Ed25519PrivateKeyParameters
import org.bouncycastle.crypto.params.HKDFParameters
import org.bouncycastle.crypto.params.KeyParameter
import org.bouncycastle.crypto.params.X25519PrivateKeyParameters
import org.bouncycastle.crypto.params.X25519PublicKeyParameters
import org.bouncycastle.crypto.signers.Ed25519Signer
import java.security.MessageDigest
import java.security.SecureRandom
import java.util.Base64

private val rng = SecureRandom()
internal fun b64(b: ByteArray): String = Base64.getEncoder().encodeToString(b)
internal fun unb64(s: String): ByteArray = Base64.getDecoder().decode(s)
fun sha256Hex(b: ByteArray): String = MessageDigest.getInstance("SHA-256").digest(b).joinToString("") { "%02x".format(it) }

/** Device identity: Ed25519 key that signs every job receipt. */
class Identity(seed: ByteArray) {
    private val priv = Ed25519PrivateKeyParameters(seed, 0)
    val seed: ByteArray = priv.encoded
    val publicKeyB64: String = b64(priv.generatePublicKey().encoded)

    fun sign(msg: String): String {
        val s = Ed25519Signer()
        s.init(true, priv)
        val bytes = msg.toByteArray()
        s.update(bytes, 0, bytes.size)
        return b64(s.generateSignature())
    }

    companion object {
        fun generate(): Identity = Identity(ByteArray(32).also { rng.nextBytes(it) })
    }
}

/**
 * Sealed jobs (ARCHITECTURE.md §3.3): a fresh X25519 key per job. Keys come from
 * HKDF-SHA256(shared, salt = job_id, info = "infera sealed job v1"): down (32) | up (32).
 */
class SandboxKey {
    private val priv = X25519PrivateKeyParameters(rng)
    val publicB64: String = b64(priv.generatePublicKey().encoded)

    fun open(jobId: String, gatewayPubB64: String): SealedChannel {
        val shared = ByteArray(32)
        priv.generateSecret(X25519PublicKeyParameters(unb64(gatewayPubB64), 0), shared, 0)
        val hk = HKDFBytesGenerator(SHA256Digest())
        hk.init(HKDFParameters(shared, jobId.toByteArray(), "infera sealed job v1".toByteArray()))
        val keys = ByteArray(64)
        hk.generateBytes(keys, 0, 64)
        return SealedChannel(keys.copyOfRange(0, 32), keys.copyOfRange(32, 64))
    }
}

class SealedChannel(private val down: ByteArray, private val up: ByteArray) {
    fun openDown(nonceB64: String, ctB64: String): ByteArray = aead(false, down, unb64(nonceB64), unb64(ctB64))

    /** Returns (nonce, ciphertext) in base64. */
    fun sealUp(plain: ByteArray): Pair<String, String> {
        val n = ByteArray(12).also { rng.nextBytes(it) }
        return b64(n) to b64(aead(true, up, n, plain))
    }

    private fun aead(encrypt: Boolean, key: ByteArray, nonce: ByteArray, input: ByteArray): ByteArray {
        val c = ChaCha20Poly1305()
        c.init(encrypt, AEADParameters(KeyParameter(key), 128, nonce))
        val out = ByteArray(c.getOutputSize(input.size))
        val n = c.processBytes(input, 0, input.size, out, 0)
        val total = n + c.doFinal(out, n)
        return out.copyOf(total)
    }
}

@Suppress("unused")
private val keepChaCha = ChaCha7539Engine::class
