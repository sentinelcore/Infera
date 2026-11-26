package network.infera.node

/** On-device engine: llama.cpp through JNI (src/main/cpp). Loads GGUF models, the same files desktops use. */
class LlamaEngine(path: String, override val modelId: String, threads: Int = bestThreads(),
                  override val parallel: Int = bestParallel()) : Engine {
    // Each request gets its own window: 4096 tokens alone, 2048 when several run at once
    // (measured: 4096 x 4 cut 4-at-once speed from 30.5 to 24.5 tok/s on a Snapdragon 8 Elite).
    private var handle: Long = nativeLoad(path, if (parallel > 1) 2048 else 4096, threads, parallel)

    fun interface TokenSink { fun onText(utf8: ByteArray): Boolean }

    override fun generate(messages: List<Pair<String, String>>, maxTokens: Int, temperature: Float, onText: (String) -> Boolean): Pair<Int, Int> {
        val r = nativeGenerate(handle, messages.map { it.first }.toTypedArray(), messages.map { it.second }.toTypedArray(),
            maxTokens, temperature, TokenSink { b -> onText(String(b, Charsets.UTF_8)) })
        return r[0] to r[1]
    }

    /** Code completion. Qwen coder models fill in the middle with their FIM tokens. */
    override fun complete(prompt: String, suffix: String, maxTokens: Int, temperature: Float, onText: (String) -> Boolean): Pair<Int, Int> {
        val text = if (suffix.isNotEmpty() && modelId.contains("coder")) "<|fim_prefix|>$prompt<|fim_suffix|>$suffix<|fim_middle|>" else prompt
        val r = nativeGenerateText(handle, text, maxTokens, temperature, TokenSink { b -> onText(String(b, Charsets.UTF_8)) })
        return r[0] to r[1]
    }

    val params: Long get() = nativeParams(handle)

    fun close() { if (handle != 0L) nativeFree(handle); handle = 0 }

    companion object {
        init { NativeLib.variant }
        /** Threads = the phone's fast cores (prime + performance), not the slow efficiency ones. */
        fun bestThreads(): Int {
            val freqs = (0 until Runtime.getRuntime().availableProcessors()).mapNotNull { i ->
                runCatching { java.io.File("/sys/devices/system/cpu/cpu$i/cpufreq/cpuinfo_max_freq").readText().trim().toLong() }.getOrNull()
            }
            if (freqs.isEmpty()) return 4
            val top = freqs.max()
            return freqs.count { it >= top * 0.7 }.coerceIn(2, 8)
        }

        @JvmStatic private external fun nativeLoad(path: String, ctx: Int, threads: Int, parallel: Int): Long

        /** Requests answered at once: more on phones with more memory (each needs its own cache). */
        fun bestParallel(): Int {
            val gb = runCatching {
                java.io.File("/proc/meminfo").readLines().first { it.startsWith("MemTotal") }.split(Regex("\\s+"))[1].toLong() / 1e6
            }.getOrDefault(4.0)
            return when { gb >= 11 -> 4; gb >= 7 -> 2; else -> 1 }
        }
        @JvmStatic private external fun nativeParams(h: Long): Long
        @JvmStatic private external fun nativeGenerate(h: Long, roles: Array<String>, contents: Array<String>, maxTokens: Int, temp: Float, sink: TokenSink): IntArray
        @JvmStatic private external fun nativeGenerateText(h: Long, text: String, maxTokens: Int, temp: Float, sink: TokenSink): IntArray
        @JvmStatic private external fun nativeFree(h: Long)
    }
}
