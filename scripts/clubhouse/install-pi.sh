#!/usr/bin/env bash
# CricketStream clubhouse screen — installer for a Raspberry Pi (or any Linux desktop).
#
# Run it on the Pi, as the user the desktop signs in as (not with sudo):
#
#   curl -fsSL https://raw.githubusercontent.com/BridestoweBelstoneCC/Cricket-Live-Stream/main/scripts/clubhouse/install-pi.sh | bash
#
# or, with the CricketStream-linux-arm64 download already beside this script:
#
#   bash install-pi.sh
#
# It puts CricketStream in ~/cricketstream, installs Chromium if it's missing, and makes
# the screen open full screen every time the Pi starts. Nothing else on the Pi changes.
# To undo it: bash install-pi.sh --uninstall
#
# See CLUBHOUSE_SCREEN.md for the whole setup, the streaming laptop's side included.
set -euo pipefail

REPO="BridestoweBelstoneCC/Cricket-Live-Stream"
DIR="$HOME/cricketstream"
BIN="$DIR/cricketstream"
AUTOSTART="$HOME/.config/autostart/cricketstream-clubhouse.desktop"
LAUNCH="$DIR/clubhouse-start.sh"

say()  { printf '  %s\n' "$*"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m⚠\033[0m %s\n' "$*"; }
die()  { printf '  \033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }

echo
echo "  CricketStream clubhouse screen"
echo "  ──────────────────────────────"

if [[ "${1:-}" == "--uninstall" ]]; then
  pkill -f "$LAUNCH" 2>/dev/null || true
  pkill -f "$BIN clubhouse" 2>/dev/null || true
  rm -f "$AUTOSTART" "$LAUNCH"
  ok "Removed the autostart entry. ~/cricketstream (and its pairing) is left in place — delete it if you like."
  exit 0
fi

[[ $EUID -ne 0 ]] || die "Run this as the Pi's desktop user, not with sudo (it asks for sudo itself if it needs to install Chromium)."

case "$(uname -m)" in
  aarch64|arm64) ASSET="CricketStream-linux-arm64" ;;
  x86_64|amd64)  ASSET="CricketStream-linux" ;;
  *) die "This is a $(uname -m) computer. CricketStream's screen needs a 64-bit Pi OS (a Pi 4 or 5) or a 64-bit PC." ;;
esac

mkdir -p "$DIR"

# ── The program ──
here="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || pwd)"
if [[ -f "$here/$ASSET" ]]; then
  install -m 755 "$here/$ASSET" "$BIN"
  ok "Installed $ASSET from beside this script"
else
  say "Downloading the newest $ASSET from GitHub..."
  # The newest release that HAS this download (a pre-release counts: the Pi build started
  # with the Go edition's betas, and "latest" skips those).
  url="$(curl -fsSL "https://api.github.com/repos/$REPO/releases?per_page=20" \
        | grep -o "\"browser_download_url\": *\"[^\"]*/$ASSET\"" | head -1 | sed 's/.*"\(https[^"]*\)"/\1/')" || true
  [[ -n "${url:-}" ]] || die "No release has $ASSET yet. Download it from https://github.com/$REPO/releases and run this script beside it."
  curl -fL --progress-bar "$url" -o "$BIN.new"
  chmod 755 "$BIN.new"
  mv "$BIN.new" "$BIN"
  ok "Downloaded $(basename "$(dirname "$url")")"
fi
"$BIN" version >/dev/null || die "The downloaded program doesn't run on this Pi — is it the 64-bit Pi OS?"

# ── Chromium, for the full-screen page ──
if command -v chromium-browser >/dev/null || command -v chromium >/dev/null; then
  ok "Chromium is installed"
else
  say "Installing Chromium (asks for your password)..."
  sudo apt-get update -qq
  sudo apt-get install -y chromium-browser 2>/dev/null || sudo apt-get install -y chromium
  ok "Installed Chromium"
fi

# ── Open on every start ──
# A desktop autostart entry: Raspberry Pi OS's desktops (labwc, Wayfire, LXDE) all run
# them. It starts a small script whose loop brings the screen back if it ever stops (a
# script, because a desktop entry's Exec line has no reliable shell quoting).
cat > "$LAUNCH" <<SCRIPT
#!/bin/sh
# Started by $AUTOSTART at every sign-in.
cd "$DIR" || exit 1
while true; do
  "$BIN" clubhouse --kiosk >> "$DIR/clubhouse.log" 2>&1
  sleep 3
done
SCRIPT
chmod 755 "$LAUNCH"
mkdir -p "$(dirname "$AUTOSTART")"
cat > "$AUTOSTART" <<ENTRY
[Desktop Entry]
Type=Application
Name=CricketStream clubhouse screen
Comment=The clubhouse touch screen for CricketStream
Exec=$LAUNCH
X-GNOME-Autostart-enabled=true
ENTRY
ok "The screen opens full screen whenever the Pi starts"

# ── The desktop must sign in by itself ──
if command -v raspi-config >/dev/null; then
  if ! grep -rqs "autologin-user=" /etc/lightdm/lightdm.conf /etc/lightdm/lightdm.conf.d 2>/dev/null; then
    warn "The Pi doesn't sign in to the desktop by itself yet. Turn it on with:"
    say  "    sudo raspi-config  →  System Options  →  Boot / Auto Login  →  Desktop Autologin"
  else
    ok "The Pi signs in to the desktop by itself"
  fi
fi

echo
say "Done. Next:"
say "  1. On the streaming laptop: CricketStream's match-day screen → Clubhouse screen → switch it on."
say "  2. Restart this Pi (sudo reboot). The screen opens and asks for the pairing key."
echo
