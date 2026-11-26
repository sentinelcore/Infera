// JNI bridge for network.infera.node.LlamaEngine
#include <jni.h>

#include <string>
#include <vector>

#include "infera_engine.h"
#include "infera_whisper.h"

using infera::Engine;
using infera::Message;

static std::string jstr(JNIEnv *env, jstring s) {
    if (!s) return "";
    const char *c = env->GetStringUTFChars(s, nullptr);
    std::string out(c);
    env->ReleaseStringUTFChars(s, c);
    return out;
}

extern "C" {

JNIEXPORT jlong JNICALL Java_network_infera_node_LlamaEngine_nativeLoad(JNIEnv *env, jclass, jstring path, jint ctx,
                                                                         jint threads, jint parallel) {
    auto *e = new Engine();
    std::string err = e->load(jstr(env, path), ctx, threads, parallel);
    if (!err.empty()) {
        delete e;
        env->ThrowNew(env->FindClass("java/lang/IllegalStateException"), err.c_str());
        return 0;
    }
    return reinterpret_cast<jlong>(e);
}

JNIEXPORT jlong JNICALL Java_network_infera_node_LlamaEngine_nativeParams(JNIEnv *, jclass, jlong h) {
    return h ? (jlong) reinterpret_cast<Engine *>(h)->n_params() : 0;
}

// callback: interface TokenSink { boolean onText(byte[] utf8); }
JNIEXPORT jintArray JNICALL Java_network_infera_node_LlamaEngine_nativeGenerate(JNIEnv *env, jclass, jlong h,
                                                                                 jobjectArray roles,
                                                                                 jobjectArray contents,
                                                                                 jint max_tokens, jfloat temp,
                                                                                 jobject sink) {
    auto *e = reinterpret_cast<Engine *>(h);
    std::vector<Message> msgs;
    jsize n = env->GetArrayLength(roles);
    for (jsize i = 0; i < n; i++) {
        auto r = (jstring)env->GetObjectArrayElement(roles, i);
        auto c = (jstring)env->GetObjectArrayElement(contents, i);
        msgs.push_back({jstr(env, r), jstr(env, c)});
        env->DeleteLocalRef(r);
        env->DeleteLocalRef(c);
    }
    jclass cls = env->GetObjectClass(sink);
    jmethodID on_text = env->GetMethodID(cls, "onText", "([B)Z");
    auto res = e->generate(msgs, max_tokens, temp, [&](const std::string &piece) {
        jbyteArray arr = env->NewByteArray((jsize)piece.size());
        env->SetByteArrayRegion(arr, 0, (jsize)piece.size(), reinterpret_cast<const jbyte *>(piece.data()));
        jboolean go = env->CallBooleanMethod(sink, on_text, arr);
        env->DeleteLocalRef(arr);
        if (env->ExceptionCheck()) return false;
        return (bool)go;
    });
    jintArray out = env->NewIntArray(2);
    jint vals[2] = {res.first, res.second};
    env->SetIntArrayRegion(out, 0, 2, vals);
    return out;
}

JNIEXPORT jintArray JNICALL Java_network_infera_node_LlamaEngine_nativeGenerateText(JNIEnv *env, jclass, jlong h, jstring text,
                                                                                     jint max_tokens, jfloat temp, jobject sink) {
    auto *e = reinterpret_cast<Engine *>(h);
    jclass cls = env->GetObjectClass(sink);
    jmethodID on_text = env->GetMethodID(cls, "onText", "([B)Z");
    auto res = e->generate_text(jstr(env, text), max_tokens, temp, [&](const std::string &piece) {
        jbyteArray arr = env->NewByteArray((jsize)piece.size());
        env->SetByteArrayRegion(arr, 0, (jsize)piece.size(), reinterpret_cast<const jbyte *>(piece.data()));
        jboolean go = env->CallBooleanMethod(sink, on_text, arr);
        env->DeleteLocalRef(arr);
        if (env->ExceptionCheck()) return false;
        return (bool)go;
    });
    jintArray out = env->NewIntArray(2);
    jint vals[2] = {res.first, res.second};
    env->SetIntArrayRegion(out, 0, 2, vals);
    return out;
}

JNIEXPORT void JNICALL Java_network_infera_node_LlamaEngine_nativeFree(JNIEnv *, jclass, jlong h) {
    delete reinterpret_cast<Engine *>(h);
}

// ---- network.infera.node.WhisperEngine (speech to text) ----
JNIEXPORT jlong JNICALL Java_network_infera_node_WhisperEngine_nativeLoad(JNIEnv *env, jclass, jstring path, jint threads) {
    auto *s = new infera::Speech();
    std::string err = s->load(jstr(env, path), threads);
    if (!err.empty()) {
        delete s;
        env->ThrowNew(env->FindClass("java/lang/IllegalStateException"), err.c_str());
        return 0;
    }
    return reinterpret_cast<jlong>(s);
}

JNIEXPORT jbyteArray JNICALL Java_network_infera_node_WhisperEngine_nativeTranscribe(JNIEnv *env, jclass, jlong h, jfloatArray pcm,
                                                                                   jstring language, jstring prompt) {
    auto *s = reinterpret_cast<infera::Speech *>(h);
    if (!s || !pcm) return env->NewByteArray(0);
    jsize n = env->GetArrayLength(pcm);
    std::vector<float> buf(n);
    env->GetFloatArrayRegion(pcm, 0, n, buf.data());
    std::string out = s->transcribe(buf, jstr(env, language), jstr(env, prompt));
    // Raw UTF-8 bytes (NewStringUTF can't take 4-byte characters).
    jbyteArray r = env->NewByteArray((jsize)out.size());
    env->SetByteArrayRegion(r, 0, (jsize)out.size(), reinterpret_cast<const jbyte *>(out.data()));
    return r;
}

JNIEXPORT void JNICALL Java_network_infera_node_WhisperEngine_nativeFree(JNIEnv *, jclass, jlong h) {
    delete reinterpret_cast<infera::Speech *>(h);
}
}
