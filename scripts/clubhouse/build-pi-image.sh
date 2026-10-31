#!/usr/bin/env bash
# Builds the clubhouse screen's Raspberry Pi image: Raspberry Pi OS (64-bit, with
# desktop) with CricketStream added, so a club flashes one file with Raspberry Pi Imager
# and the Pi boots straight to "Pair with the streaming laptop" — no terminal, no
# install-pi.sh.
#
#   sudo scripts/clubhouse/build-pi-image.sh CricketStream-linux-arm64 [out.img.xz]
#
# Runs on Linux as root (it loop-mounts the image); CI runs it on the release
# (.github/workflows/build-executables.yml). BASE_URL picks the Raspberry Pi OS image
# (default: the newest, checked against Raspberry Pi's own SHA-256).
#
# What it adds, all system-wide because Raspberry Pi Imager (or the first-boot wizard)
# creates the user later:
#   /opt/cricketstream/cricketstream          the program
#   /opt/cricketstream/clubhouse-start.sh     keeps the screen open; data in ~/cricketstream
#   /etc/xdg/autostart/cricketstream-clubhouse.desktop   opens it at every desktop sign-in
#   cricketstream-autologin.service           makes sure the desktop signs in by itself
#   /etc/cricketstream-image                  what this image is
# Nothing else changes: no password, no account, no network settings. Those come from
# Raspberry Pi Imager's own settings when it's flashed.
set -euo pipefail

BIN="${1:?usage: build-pi-image.sh <CricketStream-linux-arm64> [out.img.xz]}"
OUT="${2:-CricketStream-clubhouse-pi.img.xz}"
BASE_URL="${BASE_URL:-https://downloads.raspberrypi.com/raspios_arm64_latest}"
VERSION="${VERSION:-dev}"
GROW_MB="${GROW_MB:-256}"

[[ $EUID -eq 0 ]] || { echo "Run as root (it loop-mounts the image)." >&2; exit 1; }
[[ -f "$BIN" ]] || { echo "No program at $BIN" >&2; exit 1; }
file "$BIN" | grep -q "ARM aarch64" || { echo "$BIN isn't a 64-bit ARM Linux program" >&2; exit 1; }
for t in curl xz sfdisk losetup e2fsck resize2fs sha256sum; do
  command -v "$t" >/dev/null || { echo "Needs $t" >&2; exit 1; }
done

OUT="$(realpath -m "$OUT")"
BIN="$(realpath "$BIN")"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work="$(mktemp -d)"
root="$work/root"
loops=()
cleanup() {
  set +e
  mountpoint -q "$root/boot/firmware" && umount "$root/boot/firmware"
  mountpoint -q "$root" && umount "$root"
  for l in "${loops[@]}"; do losetup -d "$l"; done
  rm -rf "$work"
}
trap cleanup EXIT

# ── The base image, checked ──
url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$BASE_URL")"
base="$(basename "$url")"
echo "Base: $base"
curl -fL --retry 3 -o "$work/base.img.xz" "$url"
want="$(curl -fsSL --retry 3 "$url.sha256" | awk '{print $1}')"
got="$(sha256sum "$work/base.img.xz" | awk '{print $1}')"
[[ -n "$want" && "$want" == "$got" ]] || { echo "SHA-256 mismatch for $base: $got, Raspberry Pi says $want" >&2; exit 1; }
echo "SHA-256 matches Raspberry Pi's"

img="$work/image.img"
xz -dc -T0 "$work/base.img.xz" > "$img"
rm "$work/base.img.xz"

# ── Room for the program: grow the root partition (the last one) ──
# The Pi grows it to fill the card on first boot anyway; this is only so the files fit.
truncate -s "+${GROW_MB}M" "$img"
echo ", +" | sfdisk -N 2 --no-reread "$img" >/dev/null
part() { # start and size in bytes of partition $1
  sfdisk -J "$img" | python3 -c "import json,sys; p=json.load(sys.stdin)['partitiontable']; ss=p.get('sectorsize',512); q=p['partitions'][$1-1]; print(q['start']*ss, q['size']*ss)"
}
read -r boot_off boot_size < <(part 1)
read -r root_off root_size < <(part 2)
lroot="$(losetup -f --show -o "$root_off" --sizelimit "$root_size" "$img")"; loops+=("$lroot")
e2fsck -fy "$lroot" >/dev/null || [[ $? -le 1 ]]
resize2fs "$lroot" >/dev/null
lboot="$(losetup -f --show -o "$boot_off" --sizelimit "$boot_size" "$img")"; loops+=("$lboot")
mkdir -p "$root"
mount "$lroot" "$root"
mount "$lboot" "$root/boot/firmware"

# ── CricketStream ──
install -d -m 755 "$root/opt/cricketstream"
install -m 755 "$BIN" "$root/opt/cricketstream/cricketstream"
install -m 755 "$here/image/clubhouse-start.sh" "$root/opt/cricketstream/clubhouse-start.sh"
install -m 644 "$here/image/cricketstream-clubhouse.desktop" "$root/etc/xdg/autostart/cricketstream-clubhouse.desktop"
install -m 755 "$here/image/cricketstream-autologin" "$root/usr/local/sbin/cricketstream-autologin"
install -m 644 "$here/image/cricketstream-autologin.service" "$root/etc/systemd/system/cricketstream-autologin.service"
install -d "$root/etc/systemd/system/multi-user.target.wants"
ln -sf /etc/systemd/system/cricketstream-autologin.service \
  "$root/etc/systemd/system/multi-user.target.wants/cricketstream-autologin.service"
printf 'CricketStream clubhouse screen image\nversion=%s\nbase=%s\nbuilt=%s\n' \
  "$VERSION" "$base" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$root/etc/cricketstream-image"

# Chromium comes with the desktop image; say so loudly if a future one drops it.
ls "$root"/usr/bin/chromium* >/dev/null 2>&1 || echo "::warning::No Chromium in $base — the screen can't open full screen"

sync
umount "$root/boot/firmware"
umount "$root"
for l in "${loops[@]}"; do losetup -d "$l"; done
loops=()

# ── Compressed, with its checksum ──
xz -T0 -6 -c "$img" > "$OUT"
(cd "$(dirname "$OUT")" && sha256sum "$(basename "$OUT")" > "$(basename "$OUT").sha256")
ls -la "$OUT"
echo "Built $(basename "$OUT") from $base"
