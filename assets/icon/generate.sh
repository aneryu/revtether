#!/usr/bin/env bash
# Render Reverse Tether app icons from SVG into the iOS and Android catalogs.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
ICON="$ROOT/assets/icon"
IOS_SET="$ROOT/ios/ReverseTether/Assets.xcassets/AppIcon.appiconset"
ANDROID_RES="$ROOT/android/app/src/main/res"

need() { command -v "$1" >/dev/null || { echo "missing $1" >&2; exit 1; }; }
need rsvg-convert
need magick

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

render() {
  local src="$1" dest="$2"
  rsvg-convert -w 1024 -h 1024 "$src" -o "$dest"
}

opaque_png24() {
  magick "$1" -alpha off -colorspace sRGB -strip "PNG24:$2"
}

render "$ICON/AppIcon.svg" "$tmp/appicon.png"
render "$ICON/AppIconTinted.svg" "$tmp/tinted.png"
render "$ICON/AppIconForeground.svg" "$tmp/foreground.png"

mkdir -p "$IOS_SET"
opaque_png24 "$tmp/appicon.png" "$IOS_SET/AppIcon.png"
opaque_png24 "$tmp/tinted.png" "$IOS_SET/AppIconTinted.png"
magick "$tmp/foreground.png" -colorspace sRGB -strip "$tmp/foreground.png"

# Legacy launcher densities (48dp) and adaptive foreground (108dp).
# density legacy_px foreground_px
while read -r density s f; do
  dest="$ANDROID_RES/mipmap-$density"
  mkdir -p "$dest"
  r=$((s / 2))
  magick "$tmp/appicon.png" -alpha off -resize "${s}x${s}" -strip "$dest/ic_launcher.png"
  magick -size "${s}x${s}" xc:none -fill white -draw "circle ${r},${r} ${r},0" "$tmp/round-mask.png"
  magick "$dest/ic_launcher.png" "$tmp/round-mask.png" -compose CopyOpacity -composite "PNG32:$dest/ic_launcher_round.png"
  magick "$tmp/foreground.png" -resize "${f}x${f}" -strip "$dest/ic_launcher_foreground.png"
done <<'DENSITIES'
mdpi 48 108
hdpi 72 162
xhdpi 96 216
xxhdpi 144 324
xxxhdpi 192 432
DENSITIES

echo "wrote iOS $IOS_SET and Android mipmaps"

if command -v rsvg-convert >/dev/null; then
  rsvg-convert -w 44 -h 44 "$ICON/MenuBarTemplate.svg" -o "$ROOT/cmd/revtether-app/menubar.png"
  echo "wrote $ROOT/cmd/revtether-app/menubar.png"
fi

if [[ "$(uname -s)" == Darwin ]]; then
  bash "$ROOT/macos/make-icns.sh" "$ROOT/macos/AppIcon.icns"
fi
