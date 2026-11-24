plugins {
    id("org.jetbrains.kotlin.jvm")
}
kotlin { compilerOptions { jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17) } }
java {
    sourceCompatibility = JavaVersion.VERSION_17
    targetCompatibility = JavaVersion.VERSION_17
}
dependencies {
    api("com.squareup.okhttp3:okhttp:4.12.0")
    api("org.jetbrains.kotlinx:kotlinx-serialization-json:1.7.3")
    implementation("org.bouncycastle:bcprov-jdk18on:1.78.1")
    testImplementation(kotlin("test"))
}
tasks.test {
    useJUnitPlatform()
    environment("INFERA_SERVER_BIN", System.getenv("INFERA_SERVER_BIN") ?: "")
    testLogging { events("passed", "failed"); showStandardStreams = true }
}
