#!/bin/bash
# ===========================================================
#  CricketStream - Start the server only
#  quickstart.sh is the normal match-day route (it also does the
#  fixture lookup and pre-flight checks). Use this if you just
#  want the bare server running.
# ===========================================================
cd "$(dirname "$0")"
[ -f server.py ] || { [ -f ../server.py ] && cd ..; }

if [ ! -f server.py ]; then
    echo ""
    echo "  ============================================================"
    echo "   PROBLEM: I can't find the CricketStream project files"
    echo "  ============================================================"
    echo ""
    echo "   Looked in:  $(dirname "$0")"
    echo "   ...and the folder above it. Neither has server.py."
    echo ""
    echo "   TO FIX: keep this file inside the folder you unzipped -"
    echo "   either in the Mac/ folder, or next to server.py."
    echo ""
    read -p "Press Enter to close this window..."
    exit 1
fi

# macOS has its own version of the Windows Store-stub trap: /usr/bin/python3 exists on
# a Mac with no developer tools and triggers an Xcode Command Line Tools prompt instead
# of running. command -v finds it either way, so ask it to actually execute something.
if [ "$(python3 -c 'import sys;print(sys.version_info[0])' 2>/dev/null)" != "3" ]; then
    echo ""
    echo "   PROBLEM: Python 3 isn't installed."
    echo "   Run the setup wizard, or: brew install python3"
    echo ""
    read -p "Press Enter to close this window..."
    exit 1
fi

echo "  Starting CricketStream server..."
echo "  Control panel: http://localhost:5000/control"
echo "  Press Ctrl+C in this window to stop it."
echo ""
python3 server.py "$@"
status=$?
if [ $status -ne 0 ]; then
    echo ""
    echo "  ------------------------------------------------------------"
    echo "   The server stopped with an error - the reason is above."
    echo ""
    echo "   No config.ini yet? Run setup.sh first."
    echo "  ------------------------------------------------------------"
    echo ""
    read -p "Press Enter to close this window..."
fi
exit $status
