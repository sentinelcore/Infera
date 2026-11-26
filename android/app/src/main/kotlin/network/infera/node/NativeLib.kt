package network.infera.node

/**
 * Loads the phone engine once. Default: the standard dot-product build, measured fastest for
 * generating answers. Owners can opt in to the i8mm (matrix) build in Settings: it reads
 * prompts faster on newer chips but generated answers at half speed on a Snapdragon 8 Elite.
 * Takes effect when the app restarts.
 */
object NativeLib {
    @Volatile var wantMatrix = false

    fun hasI8mm(): Boolean = android.os.Build.SUPPORTED_ABIS.firstOrNull() == "arm64-v8a" && runCatching {
        java.io.File("/proc/cpuinfo").readLines().any { it.startsWith("Features") && it.split(Regex("\\s+")).contains("i8mm") }
    }.getOrDefault(false)

    val variant: String by lazy {
        if (wantMatrix && hasI8mm() && runCatching { System.loadLibrary("infera_engine_i8mm") }.isSuccess) "i8mm"
        else { System.loadLibrary("infera_engine"); "dotprod" }
    }
}
