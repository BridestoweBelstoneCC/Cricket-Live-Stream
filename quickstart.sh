#!/bin/bash
cd "$(dirname "$0")"

# Check Python is installed
if ! command -v python3 &>/dev/null; then
    echo ""
    echo "  ERROR: Python 3 not found."
    echo "  Download from https://python.org/downloads"
    echo ""
    read -p "Press Enter to exit..."
    exit 1
fi

python3 quickstart.py
read -p "Press Enter to exit..."
