plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
}

val reeveVersion = providers.gradleProperty("reeveVersion").getOrElse("0.0.0")
val versionComponent = "(0|[1-9][0-9]{0,2})"
require(Regex("$versionComponent\\.$versionComponent\\.$versionComponent").matches(reeveVersion)) {
    "reeveVersion must be a stable MAJOR.MINOR.PATCH version with components from 0 to 999"
}
val (major, minor, patch) = reeveVersion.split('.').map(String::toInt)
val reeveVersionCode = major * 1_000_000 + minor * 1_000 + patch

// AGP 要求 versionCode 为正整数；默认版本 0.0.0 取 1，正式发布（≥0.0.1）与公式一致。
val reeveVersionCodeOrOne = maxOf(1, reeveVersionCode)

val releaseKeystore = providers.environmentVariable("REEVE_ANDROID_KEYSTORE").orNull

android {
    namespace = "dev.reeve.app"
    compileSdk = 35

    defaultConfig {
        applicationId = "dev.reeve.app"
        minSdk = 26
        targetSdk = 35
        versionCode = reeveVersionCodeOrOne
        versionName = reeveVersion
    }

    signingConfigs {
        if (releaseKeystore != null) {
            create("release") {
                storeFile = file(releaseKeystore)
                storeType = "PKCS12"
                storePassword = providers.environmentVariable("REEVE_ANDROID_KEYSTORE_PASSWORD").get()
                keyAlias = providers.environmentVariable("REEVE_ANDROID_KEY_ALIAS").get()
                keyPassword = providers.environmentVariable("REEVE_ANDROID_KEY_PASSWORD").get()
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            // 没有签名密钥时保持未签名（产物 app-release-unsigned.apk），绝不回退 debug 密钥：
            // 否则每次干净构建都会生成新密钥，用户无法就地升级。
            signingConfig = signingConfigs.findByName("release")
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
    buildFeatures {
        compose = true
        buildConfig = true
    }
}

dependencies {
    val bom = platform("androidx.compose:compose-bom:2024.12.01")
    implementation(bom)
    implementation("androidx.core:core-ktx:1.15.0")
    implementation("androidx.activity:activity-compose:1.9.3")
    implementation("androidx.lifecycle:lifecycle-runtime-compose:2.8.7")
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.foundation:foundation")
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.material:material-icons-extended")
    implementation("com.squareup.okhttp3:okhttp:4.12.0")
    implementation("com.journeyapps:zxing-android-embedded:4.3.0")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.9.0")
    testImplementation("junit:junit:4.13.2")
    // android.jar 里的 org.json 在 JVM 单测中只是抛 "not mocked" 的桩，测试用真实实现。
    testImplementation("org.json:json:20240303")
}
