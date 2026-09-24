#!/bin/bash
# ===========================================================
#  CricketStream - First-time setup wizard (from source)
#  Installs packages and creates config.ini. Run this once.
# ===========================================================
cd "$(dirname "$0")"
[ -f setup_wizard.py ] || { [ -f ../setup_wizard.py ] && cd ..; }

if [ ! -f setup_wizard.py ]; then
    echo ""
    echo "  ============================================================"
    echo "   PROBLEM: I can't find the CricketStream project files"
    echo "  ============================================================"
    echo ""
    echo "   Looked in:  $(dirname "$0")"
    echo "   ...and the folder above it. Neither has setup_wizard.py."
    echo ""
    echo "   TO FIX: keep this file inside the folder you unzipped -"
    echo "   either in the Mac/ folder, or next to setup_wizard.py and"
    echo "   server.py."
    echo ""
    read -p "Press Enter to close this window..."
    exit 1
fi

# macOS has its own version of the Windows Store-stub trap: /usr/bin/python3 exists on
# a Mac with no developer tools and triggers an Xcode Command Line Tools prompt instead
# of running. command -v finds it either way, so ask it to actually execute something.
if [ "$(python3 -c 'import sys;print(sys.version_info[0])' 2>/dev/null)" != "3" ]; then
    echo ""
    echo "  ============================================================"
    echo "   PROBLEM: Python 3 isn't installed"
    echo "  ============================================================"
    echo ""
    echo "   This file is the \"I already have Python\" route. If you"
    echo "   don't have it yet, use Setup Wizard.command from the"
    echo "   release instead - it installs Python for you."
    echo ""
    echo "   Or: https://python.org/downloads  /  brew install python3"
    echo ""
    read -p "Press Enter to close this window..."
    exit 1
fi

python3 setup_wizard.py "$@"
status=$?
# The wizard pauses on its own for every outcome it knows about; this catches
# the ones it can't (e.g. python3 itself failing to start).
if [ $status -ne 0 ]; then
    echo ""
    echo "   Setup exited with an error - the reason is above."
    echo ""
    read -p "Press Enter to close this window..."
fi
exit $status
