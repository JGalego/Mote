#!/bin/sh
# Trims the empty space below the lowest line of text shown in each GIF.
# Usage: docs/demo/crop.sh docs/demo/*.gif
set -eu
for gif in "$@"; do
  crop=$(ffmpeg -v info -i "$gif" -vf "cropdetect=limit=0.16:round=2:reset=0:skip=0" -f null - 2>&1 |
    grep -o 'crop=[0-9]*:[0-9]*:[0-9]*:[0-9]*' | tail -1)
  h=$(echo "$crop" | cut -d: -f2)
  y=$(echo "$crop" | cut -d: -f4)
  [ -n "$h" ] || continue
  bottom=$((y + h + 24))
  full=$(ffprobe -v error -show_entries stream=height -of csv=p=0 "$gif")
  [ "$bottom" -lt "$full" ] || continue
  ffmpeg -v error -y -i "$gif" -vf "crop=iw:$bottom:0:0,split[a][b];[a]palettegen=stats_mode=diff[p];[b][p]paletteuse=dither=none" "$gif.tmp.gif"
  mv "$gif.tmp.gif" "$gif"
  echo "$gif: $full -> $bottom px"
done
