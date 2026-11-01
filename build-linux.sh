#!/usr/bin/env bash
# Builds the Linux version into build/linux: the Go core, the native CEF host (GTK 3, X11) and the CEF runtime.
# Run on Linux x86-64. Needs: Go 1.27.1+, gcc, pkg-config, curl, tar, and the development packages of
# GTK 3, X11, Xext, fontconfig and ALSA (Debian/Ubuntu: libgtk-3-dev libx11-dev libxext-dev libfontconfig1-dev
# libasound2-dev). At run time the program also uses, when present: zenity or kdialog (file dialogs),
# xdg-open, fontconfig's fc-list / fc-match, and wl-clipboard, xclip or xsel.
set -euo pipefail
cd "$(dirname "$0")"

version="${CEF_VERSION:-106.1.1+g5891c70+chromium-106.0.5249.119}"
distribution="cef_binary_${version}_linux64_minimal"
out="build/linux"
runtime="$out/runtime"

for tool in go gcc pkg-config curl tar strip; do
    command -v "$tool" >/dev/null || { echo "$tool is required on PATH" >&2; exit 1; }
done
pkg-config --exists gtk+-3.0 x11 xext fontconfig alsa || { echo "Install the development packages of GTK 3, X11, Xext, fontconfig and ALSA" >&2; exit 1; }
# The embedded SimUI copy is prepared on Windows by tools/prepare-ui.ps1 and kept in the repository's working tree.
[ -f internal/webui/web/simui.v5.js ] && [ -f internal/webui/web/simui.v5.css ] || { echo "internal/webui/web/simui.v5.js and .css are missing: run tools/prepare-ui.ps1 first" >&2; exit 1; }

# The pinned CEF distribution: headers to compile against, libcef.so and its resources to ship.
cef="cef/$distribution"
if [ ! -f "$cef/include/cef_api_hash.h" ]; then
    archive="cef/$distribution.tar.bz2"
    [ -f "$archive" ] || curl --fail --location --output "$archive" "https://cef-builds.spotifycdn.com/$(printf %s "$distribution.tar.bz2" | sed 's/+/%2B/g')"
    tar -xjf "$archive" -C cef
fi

# CEF ships unstripped shared libraries; strip them before linking and copying.
strip --strip-unneeded "$cef/Release/"*.so*

mkdir -p "$runtime"
# shellcheck disable=SC2046
gcc -std=gnu11 -O2 -Wall -Wno-unused-parameter -Wno-deprecated-declarations -I "$cef" cef/host_linux.c -o "$runtime/SimMusicCef" \
    $(pkg-config --cflags --libs gtk+-3.0 x11 xext fontconfig pangocairo) -L "$cef/Release" -lcef -lm -Wl,-rpath,'$ORIGIN'
cp -a "$cef/Release/." "$runtime/"
cp -a "$cef/Resources/." "$runtime/"
cp "$cef/LICENSE.txt" "$runtime/CEF-LICENSE.txt"
[ -f "$cef/CREDITS.html" ] && cp "$cef/CREDITS.html" "$runtime/"
# The host runs without Chromium's setuid sandbox helper.
rm -f "$runtime/chrome-sandbox" "$runtime"/*.a

if [ "${1:-}" != "--skip-tests" ]; then go test ./...; fi
CGO_ENABLED=1 go build -trimpath -ldflags "-s -w" -o "$out/SimMusicNext" .
echo "Built $out/SimMusicNext with Chromium 106. Distribute SimMusicNext and the runtime directory; user data is kept in $out/data."
