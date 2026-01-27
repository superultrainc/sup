#!/bin/bash
set -e

# Install sup - GitHub PR picker
# Usage: curl -sSL https://raw.githubusercontent.com/superultrainc/sup/main/install.sh | bash

REPO="superultrainc/sup"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

# Detect OS and architecture
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
    x86_64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

# Get latest release tag
LATEST=$(curl -sSL "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')

if [ -z "$LATEST" ]; then
    echo "Error: Could not fetch latest release. Building from source..."
    if ! command -v go &> /dev/null; then
        echo "Go is required to build from source. Install Go or try again later."
        exit 1
    fi
    go install "github.com/$REPO@latest"
    echo "Done! Installed via go install."
    exit 0
fi

# Download and install
URL="https://github.com/$REPO/releases/download/$LATEST/sup_${OS}_${ARCH}.tar.gz"
echo "Downloading sup $LATEST for ${OS}/${ARCH}..."

mkdir -p "$INSTALL_DIR"
curl -sSL "$URL" | tar -xz -C "$INSTALL_DIR" sup

chmod +x "$INSTALL_DIR/sup"

# Check if install dir is in PATH
if [[ ":$PATH:" != *":$INSTALL_DIR:"* ]]; then
    echo ""
    echo "Add $INSTALL_DIR to your PATH:"
    echo "  export PATH=\"\$PATH:$INSTALL_DIR\""
    echo ""
fi

echo "Done! Installed sup $LATEST to $INSTALL_DIR/sup"
