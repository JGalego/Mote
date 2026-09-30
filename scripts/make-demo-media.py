#!/usr/bin/env python3
"""Regenerate the demo media in examples/: meeting.m4a, clip.mp4, photo.jpg.

    python3 -m pip install Pillow gTTS
    python3 scripts/make-demo-media.py

The picture and the animation are drawn here, so they are ours. The speech is
made by gTTS, which sends the sentence below to Google Translate's speech
service, so it needs a network and its output is not ours to relicense; the
file names match the commands in the README (transcribe meeting.m4a,
frames clip.mp4 3, describe photo.jpg). Needs ffmpeg on the PATH.
"""
import argparse
import math
import os
import subprocess
import sys
import tempfile

from PIL import Image, ImageDraw, ImageFont

SPEECH = (
    "In today's stand-up we decided to ship the notebook console on Friday. "
    "Ana will write the release notes, and Ben will record the demo."
)

FONTS = [
    "/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
    "/usr/share/fonts/truetype/liberation2/LiberationSans-Bold.ttf",
    "/Library/Fonts/Arial Bold.ttf",
    "C:/Windows/Fonts/arialbd.ttf",
]


def font(size):
    for path in FONTS:
        if os.path.exists(path):
            return ImageFont.truetype(path, size)
    return ImageFont.load_default()


def gradient(w, h, top, bottom):
    """A sky: a vertical blend from top to bottom."""
    img = Image.new("RGB", (w, h))
    px = ImageDraw.Draw(img)
    for y in range(h):
        f = y / max(h - 1, 1)
        px.line([(0, y), (w, y)], fill=tuple(round(a + (b - a) * f) for a, b in zip(top, bottom)))
    return img


def centred(draw, box, text, fnt, fill):
    x0, y0, x1, y1 = box
    l, t, r, b = draw.textbbox((0, 0), text, font=fnt)
    draw.text(((x0 + x1 - (r - l)) / 2 - l, (y0 + y1 - (b - t)) / 2 - t), text, font=fnt, fill=fill)


def photo(path):
    """A cafe with a sign that says what it is and when it is open."""
    w, h = 800, 500
    img = gradient(w, h, (120, 180, 235), (215, 236, 250))
    d = ImageDraw.Draw(img)
    d.ellipse((640, 40, 720, 120), fill=(255, 214, 64))
    d.rectangle((0, 380, w, h), fill=(96, 160, 84))
    d.rectangle((0, 440, w, h), fill=(84, 146, 74))
    # the building
    d.rectangle((150, 190, 650, 400), fill=(196, 138, 92), outline=(120, 80, 50), width=4)
    d.polygon([(130, 190), (400, 110), (670, 190)], fill=(150, 62, 52))
    # awning stripes
    for i in range(10):
        c = (222, 70, 60) if i % 2 == 0 else (250, 244, 232)
        d.rectangle((170 + i * 46, 250, 216 + i * 46, 285), fill=c)
    d.rectangle((300, 300, 380, 400), fill=(92, 60, 40))  # door
    d.ellipse((364, 350, 374, 360), fill=(240, 200, 90))
    d.rectangle((430, 305, 600, 380), fill=(190, 226, 240), outline=(120, 80, 50), width=4)  # window
    d.line((515, 305, 515, 380), fill=(120, 80, 50), width=3)
    # the sign
    d.rectangle((230, 130, 570, 232), fill=(60, 42, 30), outline=(250, 230, 190), width=5)
    centred(d, (230, 134, 570, 190), "MOTE CAFE", font(46), (250, 230, 190))
    centred(d, (230, 186, 570, 226), "OPEN 9 TO 5", font(30), (250, 230, 190))
    # a tree
    d.rectangle((715, 300, 735, 400), fill=(110, 76, 48))
    d.ellipse((665, 210, 785, 330), fill=(64, 130, 66))
    img.save(path, quality=88, optimize=True)


def speech(path, tmp):
    """The sentence as a small mono AAC file."""
    from gtts import gTTS

    mp3 = os.path.join(tmp, "speech.mp3")
    gTTS(SPEECH, lang="en").save(mp3)
    run(["ffmpeg", "-y", "-loglevel", "error", "-i", mp3, "-ac", "1", "-ar", "24000", "-c:a", "aac", "-b:a", "40k", path])


def duration(path):
    out = subprocess.run(
        ["ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path],
        check=True, capture_output=True, text=True,
    ).stdout
    return float(out)


def frame(t, w, h):
    """One moment of the animation: a sun crossing the sky, a ball bouncing."""
    img = gradient(w, h, (110, 170, 230), (205, 230, 248))
    d = ImageDraw.Draw(img)
    sx, sy = 70 + t * (w - 140), 200 - 130 * math.sin(math.pi * t)
    d.ellipse((sx - 34, sy - 34, sx + 34, sy + 34), fill=(255, 214, 64))
    cx = (w * 0.9 - t * w * 0.5) % (w + 160) - 80  # a cloud drifting left
    for dx, dy, r in ((0, 0, 26), (30, -10, 32), (62, 0, 26)):
        d.ellipse((cx + dx - r, 70 + dy - r, cx + dx + r, 70 + dy + r), fill=(255, 255, 255))
    d.ellipse((-120, 250, 360, 520), fill=(88, 156, 82))  # hills
    d.ellipse((240, 270, 760, 540), fill=(74, 140, 72))
    bx = 120 + t * (w - 240)
    by = 268 - abs(math.sin(t * 9 * math.pi)) * 90
    d.ellipse((bx - 22, by - 22, bx + 22, by + 22), fill=(220, 60, 52))
    d.rectangle((0, h - 62, w, h), fill=(30, 34, 46))
    centred(d, (0, h - 62, w, h), "mote: small models, local machines", font(26), (240, 244, 250))
    return img


def video(path, audio, fps=15, size=(640, 360)):
    """The animation, with the speech as its soundtrack and its length."""
    w, h = size
    frames = math.ceil(duration(audio) * fps)
    enc = subprocess.Popen(
        ["ffmpeg", "-y", "-loglevel", "error", "-f", "rawvideo", "-pix_fmt", "rgb24", "-s", f"{w}x{h}",
         "-r", str(fps), "-i", "-", "-i", audio, "-c:v", "libx264", "-preset", "slow", "-crf", "30",
         "-pix_fmt", "yuv420p", "-c:a", "copy", "-shortest", "-movflags", "+faststart", path],
        stdin=subprocess.PIPE,
    )
    for i in range(frames):
        enc.stdin.write(frame(i / max(frames - 1, 1), w, h).tobytes())
    enc.stdin.close()
    if enc.wait() != 0:
        sys.exit("ffmpeg failed to encode the video")


def run(cmd):
    subprocess.run(cmd, check=True)


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--out", default=os.path.join(os.path.dirname(__file__), "..", "examples"))
    out = os.path.abspath(ap.parse_args().out)
    photo(os.path.join(out, "photo.jpg"))
    with tempfile.TemporaryDirectory() as tmp:
        audio = os.path.join(out, "meeting.m4a")
        speech(audio, tmp)
        video(os.path.join(out, "clip.mp4"), audio)
    for name in ("photo.jpg", "meeting.m4a", "clip.mp4"):
        p = os.path.join(out, name)
        print(f"{name}: {os.path.getsize(p) / 1024:.0f} KB")


if __name__ == "__main__":
    main()
