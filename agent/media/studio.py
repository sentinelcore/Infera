"""Video Studio steps for the Infera media server.

Every step downloads its inputs from signed links, does its work locally, and uploads its
output to the signed link it was given. Replies are small JSON documents.

  render   draw frames [frame_start, frame_end) of a code-described scene ("card") -> MP4
  compose  join shots, fit them to the frame, cross-fade, add voice-over and captions -> MP4
  clip     AI video shot (the media server's video model) -> MP4
  speech   voice-over (Kokoro) -> WAV, plus per-sentence timings used for captions

Rendering is deterministic: the same scene and frame number give the same pixels on every
computer (bundled font, no randomness), so ranges drawn by different devices join seamlessly.
"""
import hashlib
import math
import os
import re
import tempfile
import urllib.request

FPS = 24
_GRID = {}


def grid(w, h):
    """Cached pixel coordinates (drawing a card frame reuses them)."""
    if (w, h) not in _GRID:
        yy, xx = _np().mgrid[0:h, 0:w].astype("float32")
        _GRID[(w, h)] = (xx / w * 0.6 + yy / h * 0.4) * 0.35
    return _GRID[(w, h)]


def _np():
    import numpy as np
    return np


def font(size):
    """Pillow's bundled font: identical on every device (system fonts differ)."""
    from PIL import ImageFont
    try:
        return ImageFont.load_default(size=size)
    except TypeError:  # Pillow < 10.1
        return ImageFont.load_default()


def hex_rgb(h, fallback=(14, 20, 18)):
    try:
        h = h.lstrip("#")
        return tuple(int(h[i:i + 2], 16) for i in (0, 2, 4))
    except Exception:
        return fallback


def ease(x):
    x = max(0.0, min(1.0, x))
    return 1 - (1 - x) ** 3


def wrap(draw, text, fnt, width):
    words, lines, cur = text.split(), [], ""
    for w in words:
        t = (cur + " " + w).strip()
        if draw.textlength(t, font=fnt) <= width or not cur:
            cur = t
        else:
            lines.append(cur)
            cur = w
    if cur:
        lines.append(cur)
    return lines


# ---------------------------------------------------------------- render

def card_frame(scene, i, total, w, h):
    """One frame of a title / fact / call-to-action card."""
    from PIL import Image, ImageDraw
    np = _np()
    pal = scene.get("palette") or []
    bg, fg, ac = hex_rgb(pal[0] if pal else "#0e1412"), hex_rgb(pal[1] if len(pal) > 1 else "#f4f7f5", (244, 247, 245)), \
        hex_rgb(pal[2] if len(pal) > 2 else "#4cc398", (76, 195, 152))
    t = i / FPS
    dur = max(total / FPS, 0.1)
    # Background: diagonal gradient towards the accent colour that drifts slowly.
    phase = 0.15 * math.sin(2 * math.pi * t / max(dur, 4))
    k = np.clip(grid(w, h) + phase * 0.5, 0, 1)[..., None]
    base = np.array(bg, "float32") * (1 - k) + np.array(ac, "float32") * k * 0.55 + np.array(bg, "float32") * k * 0.45
    img = Image.fromarray(base.clip(0, 255).astype("uint8"))
    d = ImageDraw.Draw(img)
    s = min(w, h)
    # Accent bar grows in.
    bar = int(ease(t / 0.6) * w * 0.18)
    y0 = int(h * 0.40)
    d.rectangle([int(w * 0.08), y0 - int(s * 0.05), int(w * 0.08) + bar, y0 - int(s * 0.05) + max(4, s // 90)], fill=ac)
    # Headline slides up and fades in; sub-line follows.
    head = font(max(18, int(s * 0.085)))
    lines = wrap(d, scene.get("text", ""), head, w * 0.84)[:3]
    a = ease(t / 0.7)
    lift = int((1 - a) * s * 0.04)
    y = y0 + lift
    col = tuple(int(c * a + b * (1 - a)) for c, b in zip(fg, bg))
    for ln in lines:
        d.text((int(w * 0.08), y), ln, font=head, fill=col)
        y += int(head.size * 1.15) if hasattr(head, "size") else int(s * 0.1)
    sub = scene.get("sub", "")
    if sub:
        sf = font(max(12, int(s * 0.04)))
        a2 = ease((t - 0.3) / 0.7)
        col2 = tuple(int(c * a2 + b * (1 - a2)) for c, b in zip(fg, bg))
        for ln in wrap(d, sub, sf, w * 0.84)[:2]:
            d.text((int(w * 0.08), y + int(s * 0.02)), ln, font=sf, fill=col2)
            y += int(sf.size * 1.3) if hasattr(sf, "size") else int(s * 0.05)
    # Fade out over the last 0.25 s.
    out = np.asarray(img).astype("float32")
    fade = min(1.0, max(0.0, (dur - t) / 0.25))
    if fade < 1:
        out = out * fade + np.array(bg, "float32") * (1 - fade)
    return out.clip(0, 255).astype("uint8")


def render(req, upload):
    np = _np()
    import imageio_ffmpeg
    w, h = int(req["width"]), int(req["height"])
    a, b, total = int(req["frame_start"]), int(req["frame_end"]), int(req["total_frames"])
    scene = req.get("scene") or {}
    with tempfile.TemporaryDirectory() as d:
        path = os.path.join(d, "r.mp4")
        wr = imageio_ffmpeg.write_frames(path, (w, h), fps=int(req.get("fps") or FPS), codec="libx264", quality=8,
                                         macro_block_size=8, ffmpeg_log_level="error")
        wr.send(None)
        for i in range(a, b):
            wr.send(np.ascontiguousarray(card_frame(scene, i, total, w, h)))
        wr.close()
        return upload(req["output_url"], path, {"frames": b - a})


# ---------------------------------------------------------------- compose

def _download(url, path):
    with urllib.request.urlopen(url, timeout=300) as r, open(path, "wb") as f:
        while True:
            b = r.read(1 << 20)
            if not b:
                break
            f.write(b)


def _caption(img, text, w, h):
    from PIL import ImageDraw
    d = ImageDraw.Draw(img, "RGBA")
    s = min(w, h)
    f = font(max(14, int(s * 0.045)))
    lines = wrap(d, text, f, w * 0.86)[-2:]
    lh = int(getattr(f, "size", s * 0.05) * 1.3)
    y = int(h * 0.86) - lh * len(lines)
    for ln in lines:
        tw = d.textlength(ln, font=f)
        x = int((w - tw) / 2)
        pad = int(s * 0.012)
        d.rectangle([x - pad, y - pad // 2, x + tw + pad, y + lh - pad // 2], fill=(0, 0, 0, 150))
        d.text((x, y), ln, font=f, fill=(255, 255, 255, 255))
        y += lh


def compose(req, upload):
    np = _np()
    import imageio_ffmpeg
    from PIL import Image
    w, h, fps = int(req["width"]), int(req["height"]), int(req.get("fps") or FPS)
    caps = sorted(req.get("captions") or [], key=lambda c: float(c.get("start", 0)))
    with tempfile.TemporaryDirectory() as d:
        audio = None
        if req.get("audio_url"):
            audio = os.path.join(d, "voice.wav")
            _download(req["audio_url"], audio)
        out = os.path.join(d, "final.mp4")
        wr = imageio_ffmpeg.write_frames(out, (w, h), fps=fps, codec="libx264", quality=7, macro_block_size=8,
                                         audio_path=audio, audio_codec="aac" if audio else None, ffmpeg_log_level="error")
        wr.send(None)
        n, prev = 0, None
        fit = f"scale={w}:{h}:force_original_aspect_ratio=increase,crop={w}:{h},fps={fps},setsar=1"
        for k, seg in enumerate(req["segments"]):
            src = os.path.join(d, f"s{k}.mp4")
            _download(seg["url"], src)
            want = max(1, round(float(seg["seconds"]) * fps))
            probe = imageio_ffmpeg.read_frames(src)
            have = float(next(probe).get("duration") or 0)
            probe.close()
            vf = fit
            if 0 < have < want / fps <= have * 1.6:  # a little short: slow it down rather than freeze
                vf = f"setpts={want / fps / have:.4f}*PTS," + fit
            frames = imageio_ffmpeg.read_frames(src, output_params=["-vf", vf])
            meta = next(frames)  # noqa: F841 (header)
            got, last = 0, None
            for raw in frames:
                if got >= want:
                    break
                last = np.frombuffer(raw, dtype="uint8").reshape(h, w, 3)
                fr = last
                if seg.get("first_of_shot") and prev is not None and got < 6:  # short cross-fade between shots
                    a = (got + 1) / 7
                    fr = (fr.astype("float32") * a + prev.astype("float32") * (1 - a)).astype("uint8")
                wr.send(_with_caption(fr, caps, n / fps, w, h, Image))
                got, n = got + 1, n + 1
            frames.close()
            while got < want and last is not None:  # shot shorter than its slot: hold the last frame
                wr.send(_with_caption(last, caps, n / fps, w, h, Image))
                got, n = got + 1, n + 1
            if last is not None:
                prev = last
        wr.close()
        return upload(req["output_url"], out, {"frames": n, "seconds": round(n / fps, 2)})


def _with_caption(frame, caps, t, w, h, Image):
    np = _np()
    cur = [c for c in caps if float(c.get("start", 0)) <= t < float(c.get("end", 0))]
    if not cur:
        return np.ascontiguousarray(frame)
    img = Image.fromarray(frame)
    _caption(img, str(cur[-1].get("text", "")), w, h)
    return np.ascontiguousarray(np.asarray(img))


# ---------------------------------------------------------------- clip (AI shot) and speech

def mock_clip(req, path):
    """Stand-in AI shot for tests: a moving colour field, real MP4."""
    np = _np()
    import imageio_ffmpeg
    w, h = (int(x) for x in (req.get("size") or "832x480").split("x"))
    seconds = int(req.get("seconds") or 3)
    seed = int(hashlib.sha256(str(req.get("prompt", "")).encode()).hexdigest()[:6], 16)
    wr = imageio_ffmpeg.write_frames(path, (w, h), fps=16, codec="libx264", quality=6, macro_block_size=8, ffmpeg_log_level="error")
    wr.send(None)
    yy, xx = np.mgrid[0:h, 0:w].astype("float32")
    for i in range(seconds * 16 + 1):
        t = i / 16
        r = 127 + 120 * np.sin(xx / 90 + t * 1.3 + seed % 7)
        g = 127 + 120 * np.sin(yy / 70 + t * 0.9 + seed % 5)
        b = 127 + 120 * np.sin((xx + yy) / 120 + t * 0.6)
        wr.send(np.ascontiguousarray(np.stack([r, g, b], -1).clip(0, 255).astype("uint8")))
    wr.close()


def sentences(text):
    parts = [p.strip() for p in re.split(r"(?<=[.!?])\s+", text) if p.strip()]
    return parts or [text]


def speech_wav(chunks, path):
    """chunks: [(text, float32 samples at 24 kHz)] -> WAV file + caption timings."""
    import wave
    np = _np()
    t, caps, pcm = 0.0, [], []
    gap = np.zeros(int(24000 * 0.18), dtype="float32")
    for text, a in chunks:
        a = np.asarray(a, dtype="float32")
        caps.append({"text": text, "start": round(t, 2), "end": round(t + len(a) / 24000, 2)})
        t += (len(a) + len(gap)) / 24000
        pcm += [a, gap]
    s = np.concatenate(pcm) if pcm else np.zeros(2400, dtype="float32")
    with wave.open(path, "wb") as wv:
        wv.setnchannels(1); wv.setsampwidth(2); wv.setframerate(24000)
        wv.writeframes((np.clip(s, -1, 1) * 32767).astype("<i2").tobytes())
    return round(len(s) / 24000, 2), caps
