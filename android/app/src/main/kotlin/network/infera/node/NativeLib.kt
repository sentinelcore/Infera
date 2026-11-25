package network.infera.node

/**
 * Loads the phone engine once: the i8mm build on chips that have the matrix instructions
 * (much faster prompt processing), otherwise the standard dot-product build.
 */
object NativeLib {
    val variant: String by lazy {
        val i8mm = android.os.Build.SUPPORTED_ABIS.firstOrNull() == "arm64-v8a" && runCatching {
            java.io.File("/proc/cpuinfo").readLines().any { it.startsWith("Features") && it.split(Regex("\\s+")).contains("i8mm") }
        }.getOrDefault(false)
        if (i8mm && runCatching { System.loadLibrary("infera_engine_i8mm") }.isSuccess) "i8mm"
        else { System.loadLibrary("infera_engine"); "dotprod" }
    }
}
