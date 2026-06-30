#!/bin/bash
# BBCC Stream Overlay — Mac/Linux startup script
cd "$(dirname "$0")"
echo "Starting BBCC Stream Server..."
python3 server.py
