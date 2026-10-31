#!/bin/sh
# CricketStream clubhouse screen, as installed by the Raspberry Pi image
# (scripts/clubhouse/build-pi-image.sh). Started at every desktop sign-in by
# /etc/xdg/autostart/cricketstream-clubhouse.desktop; the loop brings the screen back if
# it ever stops.
#
# The program is in /opt/cricketstream; the pairing, the PIN and the log are the signed-in
# user's own, in ~/cricketstream — the same folder install-pi.sh uses. If install-pi.sh
# has been run since (to update), its own copy in ~/cricketstream is the one started.
# Not over the first-boot wizard: with no Raspberry Pi Imager settings, the Pi first signs
# in as this account to ask for a username — full screen on top, nobody could answer it.
[ "$(id -un)" = "rpi-first-boot-wizard" ] && exit 0
DIR="$HOME/cricketstream"
mkdir -p "$DIR" || exit 1
cd "$DIR" || exit 1
BIN=/opt/cricketstream/cricketstream
[ -x "$DIR/cricketstream" ] && BIN="$DIR/cricketstream"
export CRICKETSTREAM_HOME="$DIR"
while true; do
  "$BIN" clubhouse --kiosk >> "$DIR/clubhouse.log" 2>&1
  sleep 3
done
