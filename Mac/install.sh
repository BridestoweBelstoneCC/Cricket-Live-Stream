#!/bin/bash
cd "$(dirname "$0")"

echo ""
echo " CricketStream Overlay - Installing requirements"
echo " ==============================================="
echo ""

# Check Python
if ! command -v python3 &>/dev/null; then
    echo " ERROR: Python 3 not found."
    echo ""
    echo " Mac:   Download from https://python.org/downloads"
    echo "        Or install via Homebrew: brew install python3"
    echo ""
    read -p "Press Enter to exit..."
    exit 1
fi

echo " Found: $(python3 --version)"
echo ""

# Upgrade pip
echo " Updating pip..."
python3 -m pip install --upgrade pip --quiet

# Install packages
echo " Installing packages..."
echo ""
python3 -m pip install -r requirements.txt

if [ $? -ne 0 ]; then
    echo ""
    echo " ERROR: Installation failed."
    echo " Try: sudo python3 -m pip install -r requirements.txt"
    read -p "Press Enter to exit..."
    exit 1
fi

echo ""
echo " Installing SSL certificates..."
python3 -m pip install --upgrade certifi --quiet
python3 -c "import ssl, certifi; print('  SSL: certifi', certifi.__version__)" 2>/dev/null

echo ""
echo " ==============================================="
echo "  All packages installed successfully."
echo "  SSL certificates installed."
echo "  You can now run: ./quickstart.sh"
echo " ==============================================="
echo ""
read -p "Press Enter to exit..."
