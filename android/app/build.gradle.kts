plugins {
    id("com.android.application") version "8.7.3"
    id("org.jetbrains.kotlin.android") version "2.0.21"
}

android {
    namespace = "network.infera.node"
    compileSdk = 35
    defaultConfig {
        applicationId = "network.infera.node"
        minSdk = 26
        targetSdk = 35
        // Test builds: version = CI run number, so testers can see which build they have.
        val run = (System.getenv("GITHUB_RUN_NUMBER") ?: "0").toInt()
        versionCode = 1 + run
        versionName = "0.1.$run"
        buildConfigField("String", "BUILD_TIME", "\"" + java.time.ZonedDateTime.now(java.time.ZoneOffset.UTC).format(java.time.format.DateTimeFormatter.ofPattern("yyyy-MM-dd HH:mm")) + " UTC\"")
        ndk { abiFilters += listOf("arm64-v8a", "x86_64") }
        externalNativeBuild { cmake { arguments += listOf("-DANDROID_STL=c++_shared", "-DCMAKE_BUILD_TYPE=Release") } }
    }
    externalNativeBuild { cmake { path = file("src/main/cpp/CMakeLists.txt"); version = "3.22.1" } }
    // A fixed key for test builds (not secret, not for the Play Store), so each new test APK
    // installs over the previous one instead of needing an uninstall.
    signingConfigs {
        getByName("debug") {
            storeFile = file("test-signing.keystore")
            storePassword = "infera-test"
            keyAlias = "infera-test"
            keyPassword = "infera-test"
        }
    }
    buildFeatures { buildConfig = true }
    buildTypes {
        release { isMinifyEnabled = false }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
    packaging { resources { excludes += listOf("META-INF/versions/9/OSGI-INF/MANIFEST.MF", "META-INF/DEPENDENCIES") } }
}

dependencies {
    implementation(project(":core"))
}
