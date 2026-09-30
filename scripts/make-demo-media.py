#!/usr/bin/env python3
"""Regenerate the demo media in examples/: meeting.m4a, clip.mp4, photo.jpg.

    python3 -m pip install Pillow gTTS
    python3 scripts/make-demo-media.py

The picture and the animation (a sailboat at sunset, no text) are drawn here, so they are ours. The speech is
made by gTTS, which sends the two sentences below to Google Translate's speech
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

# What meeting.m4a says: something with decisions and owners to pull out.
MEETING = (
    "In today's stand-up we decided to ship the notebook console on Friday. "
    "Ana will write the release notes, and Ben will record the demo."
)

# What clip.mp4 says, over the picture it describes.
NARRATION = (
    "A small sailboat drifts across a calm sea as the sun sets behind it. "
    "A few gulls circle overhead, and the water turns from gold to deep blue."
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


def speech(text, path, tmp):
    """A sentence as a small mono AAC file."""
    from gtts import gTTS

    mp3 = os.path.join(tmp, "speech.mp3")
    gTTS(text, lang="en").save(mp3)
    run(["ffmpeg", "-y", "-loglevel", "error", "-i", mp3, "-ac", "1", "-ar", "24000", "-c:a", "aac", "-b:a", "40k", path])


def duration(path):
    out = subprocess.run(
        ["ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path],
        check=True, capture_output=True, text=True,
    ).stdout
    return float(out)


def frame(t, w, h):
    """One moment of the animation: a sailboat crossing a sea as the sun sets."""
    horizon = 232
    img = Image.new("RGBA", (w, h))
    img.paste(gradient(w, horizon, (58, 52, 120), (255, 158, 84)), (0, 0))
    sx, sy = w * 0.68, 120 + t * 108  # the sun sinks towards the horizon
    glow = Image.new("RGBA", (w, h), (0, 0, 0, 0))
    g = ImageDraw.Draw(glow)
    for r in range(150, 30, -5):
        g.ellipse((sx - r, sy - r, sx + r, sy + r), fill=(255, 196, 96, 6))
    img = Image.alpha_composite(img, glow)
    d = ImageDraw.Draw(img)
    d.ellipse((sx - 32, sy - 32, sx + 32, sy + 32), fill=(255, 226, 150, 255))

    sea = gradient(w, h - horizon, (196, 108, 88), (16, 34, 74))
    img.paste(sea, (0, horizon))
    d = ImageDraw.Draw(img)
    d.rectangle((0, horizon, w, horizon), fill=(255, 190, 120))
    for row in range(9):  # the sun's path on the water, breaking up as it nears us
        y = horizon + 6 + row * 13
        half = 34 - row * 2 + 4 * math.sin(t * 12 + row)
        d.rectangle((sx - half, y, sx + half, y + 3), fill=(255, 212, 140))
    for row in range(7):  # waves
        y = horizon + 18 + row * 18
        pts = [(x, y + 3 * math.sin(x / 26 + t * 14 + row)) for x in range(0, w + 8, 8)]
        d.line(pts, fill=(28 + row * 4, 58 + row * 5, 104 + row * 6, 255), width=2)

    bx = 90 + t * (w - 220)
    by = horizon + 30 + 4 * math.sin(t * 14)  # the boat rides the swell
    d.polygon([(bx - 44, by), (bx + 44, by), (bx + 30, by + 16), (bx - 30, by + 16)], fill=(64, 36, 30, 255))
    d.line((bx, by, bx, by - 82), fill=(40, 24, 20, 255), width=3)
    d.polygon([(bx + 3, by - 80), (bx + 3, by - 8), (bx + 46, by - 8)], fill=(250, 236, 214, 255))
    d.polygon([(bx - 3, by - 66), (bx - 3, by - 8), (bx - 34, by - 8)], fill=(232, 202, 176, 255))

    for i in range(3):  # gulls
        gx = (60 + i * 130 + t * 240) % (w + 60) - 30
        gy = 70 + i * 26 + 10 * math.sin(t * 8 + i)
        flap = 9 * math.sin(t * 60 + i * 2)
        d.line([(gx - 14, gy - flap), (gx, gy), (gx + 14, gy - flap)], fill=(30, 24, 40, 255), width=2)
    return img.convert("RGB")


def video(path, audio, fps=15, size=(640, 360)):
    """The animation, with the narration as its soundtrack and its length."""
    w, h = size
    frames = math.ceil(duration(audio) * fps)
    enc = subprocess.Popen(
        ["ffmpeg", "-y", "-loglevel", "error", "-f", "rawvideo", "-pix_fmt", "rgb24", "-s", f"{w}x{h}",
         "-r", str(fps), "-i", "-", "-i", audio, "-c:v", "libx264", "-preset", "slow", "-crf", "30",
         "-pix_fmt", "yuv420p", "-c:a", "copy", "-shortest", "-movflags", "+faststart", path],
        stdin=subprocess.PIPE,
    )
    for i in range(frames):
        img = frame(i / max(frames - 1, 1), w, h)
        if img.size != (w, h):  # ffmpeg would only warn, and write a broken video
            enc.kill()
            sys.exit(f"frame {i} is {img.size[0]}x{img.size[1]}, not {w}x{h}")
        enc.stdin.write(img.tobytes())
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
        speech(MEETING, os.path.join(out, "meeting.m4a"), tmp)
        narration = os.path.join(tmp, "narration.m4a")
        speech(NARRATION, narration, tmp)
        video(os.path.join(out, "clip.mp4"), narration)
    for name in ("photo.jpg", "meeting.m4a", "clip.mp4"):
        p = os.path.join(out, name)
        print(f"{name}: {os.path.getsize(p) / 1024:.0f} KB")


if __name__ == "__main__":
    main()
