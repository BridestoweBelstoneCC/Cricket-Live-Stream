#!/bin/bash
# ===========================================================
#  CricketStream - Match day start
#  Double-click this (or run ./quickstart.sh). It finds today's
#  fixture, starts the server and runs a pre-flight check.
#
#  Works whether it's sitting in the Mac/ folder or copied up
#  next to quickstart.py - it looks in both.
# ===========================================================
cd "$(dirname "$0")"
[ -f quickstart.py ] || { [ -f ../quickstart.py ] && cd ..; }

if [ ! -f quickstart.py ]; then
    echo ""
    echo "  ============================================================"
    echo "   PROBLEM: I can't find the CricketStream project files"
    echo "  ============================================================"
    echo ""
    echo "   Looked in:  $(dirname "$0")"
    echo "   ...and the folder above it. Neither has quickstart.py."
    echo ""
    echo "   TO FIX: keep this file inside the folder you unzipped -"
    echo "   either in the Mac/ folder, or next to quickstart.py and"
    echo "   server.py."
    echo ""
    read -p "Press Enter to close this window..."
    exit 1
fi

if ! command -v python3 &>/dev/null; then
    echo ""
    echo "  ============================================================"
    echo "   PROBLEM: Python 3 isn't installed"
    echo "  ============================================================"
    echo ""
    echo "   TO FIX, either:"
    echo "     - Run the setup wizard (Setup Wizard.command), which"
    echo "       installs Python for you, OR"
    echo "     - Download it from https://python.org/downloads, OR"
    echo "     - brew install python3"
    echo ""
    read -p "Press Enter to close this window..."
    exit 1
fi

echo ""
echo "  CricketStream Overlay - Quick Start"
echo "  ==================================="
echo ""

python3 quickstart.py "$@"
status=$?
if [ $status -ne 0 ]; then
    echo ""
    echo "  ------------------------------------------------------------"
    echo "   Quickstart stopped with an error. The reason is printed"
    echo "   above - scroll up to read it."
    echo ""
    echo "   First time here? Run setup.sh first: it installs the"
    echo "   packages and creates config.ini."
    echo "  ------------------------------------------------------------"
fi
echo ""
read -p "Press Enter to close this window..."
exit $status
