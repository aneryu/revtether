#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${1:-"$ROOT/macos/AppIcon.icns"}"
SRC_SVG="$ROOT/assets/icon/AppIcon.svg"
IOS_PNG="$ROOT/ios/ReverseTether/Assets.xcassets/AppIcon.appiconset/AppIcon.png"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

png=""
if [[ -f "$IOS_PNG" ]]; then
  png="$IOS_PNG"
else
  png="$tmp/AppIcon.png"
  if command -v rsvg-convert >/dev/null; then
    rsvg-convert -w 1024 -h 1024 "$SRC_SVG" -o "$png"
  elif command -v magick >/dev/null; then
    magick -background none -size 1024x1024 "$SRC_SVG" "$png"
  else
    echo "skip icns: need AppIcon.png, rsvg-convert, or magick" >&2
    exit 0
  fi
fi

if ! command -v sips >/dev/null || ! command -v iconutil >/dev/null; then
  echo "skip icns: need sips and iconutil" >&2
  exit 0
fi

iconset="$tmp/AppIcon.iconset"
mkdir -p "$iconset"

# size filename
while read -r size name; do
  sips -z "$size" "$size" "$png" --out "$iconset/$name" >/dev/null
done <<'SIZES'
16 icon_16x16.png
32 icon_16x16@2x.png
32 icon_32x32.png
64 icon_32x32@2x.png
128 icon_128x128.png
256 icon_128x128@2x.png
256 icon_256x256.png
512 icon_256x256@2x.png
512 icon_512x512.png
1024 icon_512x512@2x.png
SIZES

mkdir -p "$(dirname "$OUT")"
iconutil -c icns "$iconset" -o "$OUT"
echo "wrote $OUT"
