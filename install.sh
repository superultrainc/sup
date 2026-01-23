#!/bin/bash
set -e

# Install sup - GitHub PR picker

INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

echo "Building sup..."
go build -o sup .

echo "Installing to $INSTALL_DIR..."
mkdir -p "$INSTALL_DIR"
mv sup "$INSTALL_DIR/sup"

# Check if install dir is in PATH
if [[ ":$PATH:" != *":$INSTALL_DIR:"* ]]; then
    echo ""
    echo "Add $INSTALL_DIR to your PATH:"
    echo "  export PATH=\"\$PATH:$INSTALL_DIR\""
    echo ""
fi

echo "Done! Run: sup"
