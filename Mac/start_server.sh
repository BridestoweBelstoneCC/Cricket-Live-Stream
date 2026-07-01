#!/bin/bash
# CricketStream Overlay — Mac/Linux startup script
cd "$(dirname "$0")"
echo "Starting CricketStream Server..."
python3 server.py
