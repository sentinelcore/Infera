package network.infera.node

/** On-device engine: llama.cpp through JNI (src/main/cpp). Loads GGUF models, the same files desktops use. */
class LlamaEngine(path: String, override val modelId: String, ctx: Int = 4096, threads: Int = bestThreads()) : Engine {
    companion object {
        /** Threads = the phone's fast cores (prime + performance), not the slow efficiency ones. */
        fun bestThreads(): Int {
            val freqs = (0 until Runtime.getRuntime().availableProcessors()).mapNotNull { i ->
                runCatching { java.io.File("/sys/devices/system/cpu/cpu$i/cpufreq/cpuinfo_max_freq").readText().trim().toLong() }.getOrNull()
            }
            if (freqs.isEmpty()) return 4
            val top = freqs.max()
            return freqs.count { it >= top * 0.7 }.coerceIn(2, 8)
        }
    }

    private var handle: Long = nativeLoad(path, ctx, threads)

    fun interface TokenSink { fun onText(utf8: ByteArray): Boolean }

    override fun generate(messages: List<Pair<String, String>>, maxTokens: Int, temperature: Float, onText: (String) -> Boolean): Pair<Int, Int> {
        val r = nativeGenerate(handle, messages.map { it.first }.toTypedArray(), messages.map { it.second }.toTypedArray(),
            maxTokens, temperature, TokenSink { b -> onText(String(b, Charsets.UTF_8)) })
        return r[0] to r[1]
    }

    val params: Long get() = nativeParams(handle)

    fun close() { if (handle != 0L) nativeFree(handle); handle = 0 }

    companion object {
        init { System.loadLibrary("infera_engine") }
        @JvmStatic private external fun nativeLoad(path: String, ctx: Int, threads: Int): Long
        @JvmStatic private external fun nativeParams(h: Long): Long
        @JvmStatic private external fun nativeGenerate(h: Long, roles: Array<String>, contents: Array<String>, maxTokens: Int, temp: Float, sink: TokenSink): IntArray
        @JvmStatic private external fun nativeFree(h: Long)
    }
}
