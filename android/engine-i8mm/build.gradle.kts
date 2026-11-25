// The phone engine built for chips with the i8mm matrix instructions (ARMv8.6+: Snapdragon
// 8 Gen 1 and later, Dimensity 9000+, Tensor G2+). Same sources as the app's engine; the app
// loads this one only when /proc/cpuinfo lists i8mm.
plugins {
    id("com.android.library") version "8.7.3"
}

android {
    namespace = "network.infera.engine.i8mm"
    compileSdk = 35
    defaultConfig {
        minSdk = 26
        ndk { abiFilters += listOf("arm64-v8a") }
        externalNativeBuild {
            cmake {
                arguments += listOf("-DANDROID_STL=c++_shared", "-DCMAKE_BUILD_TYPE=Release",
                    "-DINFERA_LIB_NAME=infera_engine_i8mm", "-DINFERA_ARM_ARCH=armv8.6-a+dotprod+i8mm+fp16")
            }
        }
    }
    externalNativeBuild { cmake { path = file("../app/src/main/cpp/CMakeLists.txt"); version = "3.22.1" } }
}
