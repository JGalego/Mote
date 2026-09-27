#!/bin/sh
# Creates the input files used by the demo recordings in ~/demo.
# Run after `mote setup` with the throwaway HOME from common.tape.
set -eu
# Resolved before changing directory, since $0 may be a relative path.
repo=$(cd "$(dirname "$0")/../.." && pwd)
mkdir -p ~/demo && cd ~/demo
font=/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf
ffmpeg -v error -y -f lavfi -i color=c=0x2e4a3a:s=640x400 -frames:v 1 \
  -vf "drawtext=fontfile=$font:text='CORNER CAFE':fontcolor=white:fontsize=56:x=(w-tw)/2:y=90,drawtext=fontfile=$font:text='Open daily 7\:30 - 18\:00':fontcolor=0xf4d35e:fontsize=36:x=(w-tw)/2:y=210,drawtext=fontfile=$font:text='Closed on public holidays':fontcolor=white:fontsize=26:x=(w-tw)/2:y=290" \
  sign.png
cat > slugify.py <<'PY'
import re


def slugify(text, sep="-"):
    text = text.lower().strip()
    text = re.sub(r"[^a-z0-9]+", sep, text)
    return text.strip(sep)
PY
cp "$repo/examples/invoice.txt" "$repo/examples/invoice.schema.json" .
mote run speak "Quick update from the team meeting. The release moves to Thursday, and Maria will handle the migration." -o meeting.wav
ffmpeg -v error -y -f lavfi -i "testsrc2=size=640x400:rate=10:duration=4" -loop 1 -t 4 -i sign.png -i meeting.wav \
  -filter_complex "[0:v][1:v]concat=n=2:v=1[v]" -map "[v]" -map 2:a -shortest -pix_fmt yuv420p clip.mp4
rm -rf shop && mkdir shop
cat > shop/cart.py <<'PY'
def add_item(items, name, price, qty=1):
    items.append({"name": name, "price": price, "qty": qty})
    return items
PY
git -C shop init -q && git -C shop add . && git -C shop -c user.name=demo -c user.email=demo@example.org commit -qm init
