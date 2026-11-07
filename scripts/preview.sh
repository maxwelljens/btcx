#!/usr/bin/env bash
# Capture docs/preview.png: the main window with example mail and a compose
# window layered over it, for the README.
#
# Requires ffmpeg, ImageMagick (magick), xrandr, and an X display. On
# Wayland desktops XWayland provides one; the GTK windows are forced onto
# it so the X root holds exactly this scene and nothing else.
set -euo pipefail
cd "$(dirname "$0")/.."

for tool in ffmpeg magick go; do
	command -v "$tool" >/dev/null || { echo "missing $tool" >&2; exit 1; }
done
[ -n "${DISPLAY:-}" ] || { echo "no X display (DISPLAY unset)" >&2; exit 1; }

export GDK_BACKEND=x11
export BTCX_PREVIEW=1
export BTCX_PREVIEW_OUT="$(cd "$(dirname "${1:-docs/preview.png}")" 2>/dev/null && pwd)/$(basename "${1:-docs/preview.png}")"

go test -count=1 -timeout=5m -run '^TestPreviewScreenshot$' -v ./internal/ui/
echo "wrote ${BTCX_PREVIEW_OUT}"
