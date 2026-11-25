package network.infera.node

import android.media.AudioFormat
import android.media.MediaCodec
import android.media.MediaExtractor
import android.media.MediaFormat
import java.io.File
import java.nio.ByteOrder

/** On-device speech to text: whisper.cpp through JNI (src/main/cpp/infera_whisper.cpp). */
class WhisperEngine(path: String, override val modelId: String, private val cacheDir: File,
                    threads: Int = LlamaEngine.bestThreads()) : SpeechEngine {
    private var handle: Long = nativeLoad(path, threads)

    override fun transcribe(audio: ByteArray, filename: String, language: String, prompt: String): Pair<String, Int> {
        val pcm = decodeTo16kMono(audio, filename, cacheDir)
        val out = String(nativeTranscribe(handle, pcm, language, prompt), Charsets.UTF_8)
        if (out.isEmpty()) throw IllegalStateException("speech engine failed")
        return out to kotlin.math.ceil(pcm.size / 16000.0).toInt().coerceAtLeast(1)
    }

    fun close() { if (handle != 0L) nativeFree(handle); handle = 0 }

    companion object {
        init { NativeLib.variant }
        @JvmStatic private external fun nativeLoad(path: String, threads: Int): Long
        @JvmStatic private external fun nativeTranscribe(h: Long, pcm: FloatArray, language: String, prompt: String): ByteArray
        @JvmStatic private external fun nativeFree(h: Long)

        /**
         * Decodes any audio Android can read (mp3, m4a/aac, ogg/opus, flac, wav, webm…) with the
         * phone's own codecs, mixes it to mono and resamples to 16 kHz, as Whisper expects.
         */
        fun decodeTo16kMono(audio: ByteArray, filename: String, cacheDir: File): FloatArray {
            val ext = filename.substringAfterLast('.', "bin").take(8)
            val tmp = File.createTempFile("job-audio", ".$ext", cacheDir)
            try {
                tmp.writeBytes(audio)
                val ex = MediaExtractor()
                ex.setDataSource(tmp.path)
                val track = (0 until ex.trackCount).firstOrNull { ex.getTrackFormat(it).getString(MediaFormat.KEY_MIME)?.startsWith("audio/") == true }
                    ?: throw IllegalStateException("no audio track in the file")
                ex.selectTrack(track)
                val fmt = ex.getTrackFormat(track)
                var rate = fmt.getInteger(MediaFormat.KEY_SAMPLE_RATE)
                var channels = fmt.getInteger(MediaFormat.KEY_CHANNEL_COUNT)
                var float = false
                val codec = MediaCodec.createDecoderByType(fmt.getString(MediaFormat.KEY_MIME)!!)
                codec.configure(fmt, null, null, 0)
                codec.start()
                val mono = FloatList(rate * 60)
                val info = MediaCodec.BufferInfo()
                var inputDone = false
                var outputDone = false
                while (!outputDone) {
                    if (!inputDone) {
                        val i = codec.dequeueInputBuffer(10_000)
                        if (i >= 0) {
                            val buf = codec.getInputBuffer(i)!!
                            val n = ex.readSampleData(buf, 0)
                            if (n < 0) { codec.queueInputBuffer(i, 0, 0, 0, MediaCodec.BUFFER_FLAG_END_OF_STREAM); inputDone = true }
                            else { codec.queueInputBuffer(i, 0, n, ex.sampleTime, 0); ex.advance() }
                        }
                    }
                    val o = codec.dequeueOutputBuffer(info, 10_000)
                    if (o == MediaCodec.INFO_OUTPUT_FORMAT_CHANGED) {
                        val f = codec.outputFormat
                        rate = f.getInteger(MediaFormat.KEY_SAMPLE_RATE); channels = f.getInteger(MediaFormat.KEY_CHANNEL_COUNT)
                        float = f.containsKey(MediaFormat.KEY_PCM_ENCODING) && f.getInteger(MediaFormat.KEY_PCM_ENCODING) == AudioFormat.ENCODING_PCM_FLOAT
                    } else if (o >= 0) {
                        val b = codec.getOutputBuffer(o)!!.order(ByteOrder.LITTLE_ENDIAN)
                        b.position(info.offset); b.limit(info.offset + info.size)
                        if (float) {
                            val fb = b.asFloatBuffer()
                            while (fb.remaining() >= channels) { var s = 0f; repeat(channels) { s += fb.get() }; mono.add(s / channels) }
                        } else {
                            val sb = b.asShortBuffer()
                            while (sb.remaining() >= channels) { var s = 0f; repeat(channels) { s += sb.get() / 32768f }; mono.add(s / channels) }
                        }
                        codec.releaseOutputBuffer(o, false)
                        if (info.flags and MediaCodec.BUFFER_FLAG_END_OF_STREAM != 0) outputDone = true
                    }
                    if (mono.size > rate * 60 * 60) throw IllegalStateException("audio longer than an hour")
                }
                codec.stop(); codec.release(); ex.release()
                // Linear resample to 16 kHz.
                if (rate == 16000) return mono.toArray()
                val outLen = (mono.size.toLong() * 16000 / rate).toInt()
                val out = FloatArray(outLen)
                for (k in 0 until outLen) {
                    val x = k.toDouble() * rate / 16000
                    val i = x.toInt(); val f = (x - i).toFloat()
                    val a = mono[i.coerceAtMost(mono.size - 1)]; val c = mono[(i + 1).coerceAtMost(mono.size - 1)]
                    out[k] = a + (c - a) * f
                }
                return out
            } finally {
                tmp.delete()
            }
        }
    }
}

/** Growable float array (avoids boxing millions of samples). */
private class FloatList(cap: Int) {
    private var a = FloatArray(cap.coerceAtLeast(1024))
    var size = 0; private set
    fun add(v: Float) { if (size == a.size) a = a.copyOf(a.size * 2); a[size++] = v }
    operator fun get(i: Int) = a[i]
    fun toArray(): FloatArray = a.copyOf(size)
}
