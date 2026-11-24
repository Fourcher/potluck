#!/bin/sh
# Installs Potluck on Linux for the current user (no root needed):
#
#   curl -fsSL https://github.com/Fourcher/potluck/releases/latest/download/install.sh | sh
#
# It downloads the Potluck program for this computer, checks it against the release's
# checksums, puts it in ~/.local/bin, adds Potluck to the applications menu, and starts it.
# To remove Potluck: rm ~/.local/bin/potluck ~/.local/share/applications/potluck.desktop
# and the ~/Potluck folder (its settings and models).
set -eu

REPO="Fourcher/potluck"
BASE="https://github.com/$REPO/releases/latest/download"
BIN_DIR="${POTLUCK_BIN_DIR:-$HOME/.local/bin}"
DATA_DIR="${POTLUCK_DATA:-$HOME/Potluck}"

say() { printf '%s\n' "$*"; }
fail() { printf 'Potluck install: %s\n' "$*" >&2; exit 1; }

[ "$(uname -s)" = Linux ] || fail "this installer is for Linux. Mac and Windows downloads are at https://github.com/$REPO/releases/latest"
case "$(uname -m)" in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) fail "Potluck has no build for $(uname -m) yet" ;;
esac
FILE="potluck-linux-$ARCH"

if command -v curl >/dev/null 2>&1; then
  get() { curl -fsSL -A potluck-installer -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
  get() { wget -q -U potluck-installer -O "$2" "$1"; }
else
  fail "needs curl or wget"
fi

TMP=$(mktemp -d 2>/dev/null || mktemp -d -t potluck)
trap 'rm -rf "$TMP"' EXIT INT TERM

say "Downloading Potluck for Linux ($ARCH)..."
get "$BASE/$FILE" "$TMP/$FILE" || fail "download failed"
get "$BASE/SHA256SUMS.txt" "$TMP/SHA256SUMS.txt" || fail "could not download the checksums"

WANT=$(grep " $FILE\$" "$TMP/SHA256SUMS.txt" | head -n 1 | cut -d ' ' -f 1)
[ -n "$WANT" ] || fail "the release has no checksum for $FILE"
if command -v sha256sum >/dev/null 2>&1; then
  GOT=$(sha256sum "$TMP/$FILE" | cut -d ' ' -f 1)
elif command -v shasum >/dev/null 2>&1; then
  GOT=$(shasum -a 256 "$TMP/$FILE" | cut -d ' ' -f 1)
else
  GOT=$(openssl dgst -sha256 "$TMP/$FILE" | sed 's/.*= //')
fi
[ "$WANT" = "$GOT" ] || fail "the download did not match its checksum, so it was not installed"

mkdir -p "$BIN_DIR"
# Stop a Potluck that is already running from here, so the new one can take its place.
if [ -x "$BIN_DIR/potluck" ] && command -v pkill >/dev/null 2>&1; then
  pkill -f "^$BIN_DIR/potluck" 2>/dev/null || true
fi
mv "$TMP/$FILE" "$BIN_DIR/potluck"
chmod 755 "$BIN_DIR/potluck"
say "Installed $("$BIN_DIR/potluck" version 2>/dev/null || echo Potluck) to $BIN_DIR/potluck"

# A menu entry on desktops.
APPS="${XDG_DATA_HOME:-$HOME/.local/share}/applications"
if [ -n "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ] || [ -d "$APPS" ]; then
  mkdir -p "$APPS"
  cat > "$APPS/potluck.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=Potluck
Comment=Private AI on the computers you already own
Exec=$BIN_DIR/potluck --data $DATA_DIR
Terminal=false
Categories=Utility;Network;
EOF
  say "Added Potluck to your applications menu."
fi

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) say "Note: $BIN_DIR is not on your PATH. Add it, or run $BIN_DIR/potluck directly." ;;
esac

# Start it now, in the background, unless asked not to (POTLUCK_NO_START=1).
if [ "${POTLUCK_NO_START:-}" != 1 ]; then
  mkdir -p "$DATA_DIR/logs"
  if [ -n "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ]; then
    nohup "$BIN_DIR/potluck" --data "$DATA_DIR" >> "$DATA_DIR/logs/potluck.log" 2>&1 < /dev/null &
    say "Potluck is starting. Its control panel opens in your browser: http://127.0.0.1:7716"
  else
    nohup "$BIN_DIR/potluck" --data "$DATA_DIR" --no-browser >> "$DATA_DIR/logs/potluck.log" 2>&1 < /dev/null &
    say "Potluck is running. No desktop here, so open http://127.0.0.1:7716 on this computer"
    say "(or through an SSH tunnel), or join it to your main computer with:"
    say "  $BIN_DIR/potluck join --main <main computer's address> --data $DATA_DIR"
  fi
fi
