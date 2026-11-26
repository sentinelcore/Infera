"""Infera media server: text to speech, text to video and speech to text on this computer's GPU.

Started and stopped by the Infera node (`infera setup` installs it with its own Python).
Listens on 127.0.0.1 only. Endpoints (OpenAI-shaped, used by the node, not by customers):

  GET  /v1/models                   models this server serves
  POST /v1/audio/speech             {"input","voice","response_format","speed"} -> audio bytes
  POST /v1/videos/generate          {"prompt","seconds","size","negative_prompt","seed"} -> MP4 bytes
  POST /v1/audio/transcriptions     multipart: file, language, prompt -> verbose JSON

Models load on first use and stay loaded. --mock answers instantly with stand-in output (tests).
"""
import argparse
import email.parser
import email.policy
import io
import json
import os
import subprocess
import sys
import tempfile
import threading
import wave
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ARGS = None
LOCK = threading.Lock()  # one GPU job at a time
_tts = _video = _stt = None

VOICES = {"alloy": "af_heart", "ash": "am_michael", "ballad": "bm_george", "coral": "af_bella", "echo": "am_fenrir",
          "fable": "bf_emma", "nova": "af_nova", "onyx": "am_onyx", "sage": "af_sarah", "shimmer": "af_sky"}


def ffmpeg():
    try:
        import imageio_ffmpeg
        return imageio_ffmpeg.get_ffmpeg_exe()
    except Exception:
        return "ffmpeg"


def encode_audio(pcm, rate, fmt):
    """16-bit mono PCM -> bytes in the requested format (wav/pcm directly, others via ffmpeg)."""
    if fmt == "pcm":
        return pcm
    buf = io.BytesIO()
    with wave.open(buf, "wb") as w:
        w.setnchannels(1); w.setsampwidth(2); w.setframerate(rate); w.writeframes(pcm)
    if fmt == "wav":
        return buf.getvalue()
    codec = {"mp3": ["-f", "mp3", "-b:a", "128k"], "opus": ["-f", "ogg", "-c:a", "libopus"], "flac": ["-f", "flac"],
             "aac": ["-f", "adts", "-c:a", "aac"]}[fmt]
    p = subprocess.run([ffmpeg(), "-loglevel", "error", "-i", "pipe:0", *codec, "pipe:1"], input=buf.getvalue(), capture_output=True)
    if p.returncode != 0:
        raise RuntimeError("audio encoding failed: " + p.stderr.decode()[-300:])
    return p.stdout


def speech(req):
    text, fmt = req.get("input", ""), req.get("response_format") or "mp3"
    if ARGS.mock:  # a short tone, as WAV
        import math, struct
        pcm = b"".join(struct.pack("<h", int(6000 * math.sin(i / 8))) for i in range(2400 * max(1, len(text) // 15)))
        return encode_audio(pcm, 24000, "pcm" if fmt == "pcm" else "wav"), ("pcm" if fmt == "pcm" else "wav")
    global _tts
    if _tts is None:
        from kokoro import KPipeline
        _tts = KPipeline(lang_code="a", repo_id="hexgrad/Kokoro-82M", device=ARGS.device if ARGS.device != "mps" else "cpu")
    import numpy as np
    voice = req.get("voice") or "alloy"
    voice = VOICES.get(voice, voice)
    parts = [a.numpy() if hasattr(a, "numpy") else a for _, _, a in _tts(text, voice=voice, speed=float(req.get("speed") or 1))]
    samples = np.concatenate(parts) if parts else np.zeros(2400, dtype="float32")
    pcm = (np.clip(samples, -1, 1) * 32767).astype("<i2").tobytes()
    return encode_audio(pcm, 24000, fmt), fmt


def video(req):
    seconds = max(1, min(5, int(req.get("seconds") or 5)))
    w, h = (int(x) for x in (req.get("size") or "832x480").split("x"))
    if ARGS.mock:
        return b"\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom" + bytes(range(256)) * (3 * 4096)  # ~3 MB: arrives in several pieces
    global _video
    import torch
    if _video is None:
        from diffusers import AutoencoderKLWan, WanPipeline
        mid = "Wan-AI/Wan2.1-T2V-1.3B-Diffusers"
        vae = AutoencoderKLWan.from_pretrained(mid, subfolder="vae", torch_dtype=torch.float32)
        _video = WanPipeline.from_pretrained(mid, vae=vae, torch_dtype=torch.bfloat16)
        if ARGS.vram >= 12:
            _video.to(ARGS.device)
        else:
            _video.enable_model_cpu_offload()  # fits 8 GB cards
    from diffusers.utils import export_to_video
    frames = seconds * 16 + 1  # Wan: 16 fps, 4k+1 frames
    frames = (frames - 1) // 4 * 4 + 1
    gen = torch.Generator(device="cpu").manual_seed(int(req["seed"])) if req.get("seed") is not None else None
    out = _video(prompt=req["prompt"], negative_prompt=req.get("negative_prompt") or None, height=h, width=w,
                 num_frames=frames, guidance_scale=5.0, generator=gen).frames[0]
    with tempfile.TemporaryDirectory() as d:
        path = os.path.join(d, "v.mp4")
        export_to_video(out, path, fps=16)
        return open(path, "rb").read()


def transcribe(fields):
    audio, lang, prompt = fields.get("file", b""), fields.get("language") or None, fields.get("prompt") or None
    if ARGS.mock:
        return {"text": " Hello from the audio file.", "language": "en", "duration": round(len(audio) / 32000, 2), "segments": []}
    global _stt
    if _stt is None:
        from faster_whisper import WhisperModel
        _stt = WhisperModel(ARGS.stt, device="cuda" if ARGS.device == "cuda" else "cpu",
                            compute_type="float16" if ARGS.device == "cuda" else "int8")
    segs, info = _stt.transcribe(io.BytesIO(audio), language=lang, initial_prompt=prompt, vad_filter=True)
    segs = [{"id": i, "start": round(s.start, 2), "end": round(s.end, 2), "text": s.text} for i, s in enumerate(segs)]
    return {"text": "".join(s["text"] for s in segs), "language": info.language, "duration": round(info.duration, 2), "segments": segs}


def multipart(body, ctype):
    msg = email.parser.BytesParser(policy=email.policy.HTTP).parsebytes(b"Content-Type: " + ctype.encode() + b"\r\n\r\n" + body)
    out = {}
    for part in msg.iter_parts():
        name = part.get_param("name", header="content-disposition")
        data = part.get_payload(decode=True) or b""
        out[name] = data if name == "file" else data.decode(errors="replace")
    return out


class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def reply(self, code, body, ctype="application/json"):
        if isinstance(body, (dict, list)):
            body = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path.startswith("/v1/models") or self.path == "/health":
            models = [m for m in (ARGS.tts_id, ARGS.video_id, ARGS.stt_id) if m]
            return self.reply(200, {"object": "list", "data": [{"id": m, "object": "model"} for m in models]})
        self.reply(404, {"error": "not found"})

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length") or 0))
        try:
            with LOCK:
                if self.path == "/v1/audio/speech" and ARGS.tts_id:
                    audio, fmt = speech(json.loads(body))
                    return self.reply(200, audio, "audio/" + fmt)
                if self.path == "/v1/videos/generate" and ARGS.video_id:
                    return self.reply(200, video(json.loads(body)), "video/mp4")
                if self.path == "/v1/audio/transcriptions" and ARGS.stt_id:
                    return self.reply(200, transcribe(multipart(body, self.headers["Content-Type"])))
            self.reply(404, {"error": "this server doesn't serve " + self.path})
        except Exception as e:  # report, keep serving
            print("error:", repr(e), file=sys.stderr, flush=True)
            self.reply(500, {"error": str(e)[:500]})


def main():
    global ARGS
    p = argparse.ArgumentParser()
    p.add_argument("--port", type=int, default=18179)
    p.add_argument("--device", default="cuda")
    p.add_argument("--vram", type=float, default=8)
    p.add_argument("--tts-id", default="", help="catalog name for text to speech, e.g. kokoro-82m")
    p.add_argument("--video-id", default="", help="catalog name for text to video, e.g. wan2.1-t2v-1.3b")
    p.add_argument("--stt-id", default="", help="catalog name for speech to text, e.g. whisper-large-v3-turbo")
    p.add_argument("--stt", default="large-v3-turbo", help="faster-whisper model")
    p.add_argument("--mock", action="store_true")
    p.add_argument("--check", action="store_true", help="import everything and exit (used by setup)")
    ARGS = p.parse_args()
    if ARGS.check:
        if ARGS.tts_id:
            import kokoro  # noqa: F401
        if ARGS.video_id:
            import diffusers, torch  # noqa: F401,E401
            assert torch.cuda.is_available() or ARGS.device != "cuda", "PyTorch can't see the GPU"
        if ARGS.stt_id:
            import faster_whisper  # noqa: F401
        print("ok")
        return
    ThreadingHTTPServer(("127.0.0.1", ARGS.port), H).serve_forever()


if __name__ == "__main__":
    main()
