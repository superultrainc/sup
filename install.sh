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
    echo "Could not fetch latest release. Building from source..."
    if ! command -v go &> /dev/null; then
        echo "Go is required to build from source. Install Go or try again later."
        exit 1
    fi
    go install "github.com/$REPO@latest"
    INSTALL_DIR="$(go env GOPATH)/bin"
else
    # Download and install
    URL="https://github.com/$REPO/releases/download/$LATEST/sup_${OS}_${ARCH}.tar.gz"
    echo "Downloading sup $LATEST for ${OS}/${ARCH}..."

    mkdir -p "$INSTALL_DIR"
    curl -sSL "$URL" | tar -xz -C "$INSTALL_DIR" sup

    chmod +x "$INSTALL_DIR/sup"
fi

# Check if install dir is in PATH
if [[ ":$PATH:" != *":$INSTALL_DIR:"* ]]; then
    echo ""
    echo "Add $INSTALL_DIR to your PATH:"
    echo "  export PATH=\"\$PATH:$INSTALL_DIR\""
fi

# Detect shell config file
SHELL_NAME=$(basename "$SHELL")
case "$SHELL_NAME" in
    zsh)  SHELL_RC="$HOME/.zshrc" ;;
    bash) SHELL_RC="$HOME/.bashrc" ;;
    *)    SHELL_RC="$HOME/.${SHELL_NAME}rc" ;;
esac

# Shell wrapper for cd integration
SHELL_WRAPPER='
# sup - GitHub PR picker
sup() {
  rm -f /tmp/sup-selection
  command sup "$@"
  if [[ -f /tmp/sup-selection ]]; then
    cd "$(cat /tmp/sup-selection)"
    rm -f /tmp/sup-selection
  fi
}'

# Clean up old gpr function if present (renamed to sup)
if grep -q "gpr()" "$SHELL_RC" 2>/dev/null; then
    sed -i.bak '/# gpr - GitHub PR picker/,/^}/d' "$SHELL_RC"
    rm -f "$SHELL_RC.bak"
    echo "Removed old gpr() function from $SHELL_RC"
fi

# Add shell wrapper if not already present
if grep -q "sup()" "$SHELL_RC" 2>/dev/null; then
    echo ""
    echo "Shell wrapper already exists in $SHELL_RC"
else
    echo "$SHELL_WRAPPER" >> "$SHELL_RC"
    echo ""
    echo "Added shell wrapper to $SHELL_RC"
    echo "Run: source $SHELL_RC"
fi

echo ""
echo "Done! Installed sup to $INSTALL_DIR/sup"
