#!/bin/bash
set -e

# Install gpr - GitHub PR picker

INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

# Prompt for dev directory
echo "Where are your git repos located?"
read -p "Dev directory [$HOME/Development]: " DEV_DIR
DEV_DIR="${DEV_DIR:-$HOME/Development}"

# Expand ~ if used
DEV_DIR="${DEV_DIR/#\~/$HOME}"

echo ""
echo "Building gpr..."
go build -o gpr .

echo "Installing to $INSTALL_DIR..."
mkdir -p "$INSTALL_DIR"
mv gpr "$INSTALL_DIR/gpr"

# Detect shell and config file
SHELL_NAME=$(basename "$SHELL")
case "$SHELL_NAME" in
    zsh)  SHELL_RC="$HOME/.zshrc" ;;
    bash) SHELL_RC="$HOME/.bashrc" ;;
    *)    SHELL_RC="$HOME/.${SHELL_NAME}rc" ;;
esac

# Shell config to add
SHELL_CONFIG="
# gpr - GitHub PR picker
export GPR_DEV_DIR=\"$DEV_DIR\"
gpr() {
  rm -f /tmp/gpr-selection
  \"$INSTALL_DIR/gpr\"
  if [[ -f /tmp/gpr-selection ]]; then
    cd \"\$(cat /tmp/gpr-selection)\"
    rm -f /tmp/gpr-selection
  fi
}"

# Check if already installed
if grep -q "GPR_DEV_DIR" "$SHELL_RC" 2>/dev/null; then
    echo "gpr config already exists in $SHELL_RC - please update manually if needed"
else
    echo "Adding gpr config to $SHELL_RC..."
    echo "$SHELL_CONFIG" >> "$SHELL_RC"
fi

# Ensure install dir is in PATH
if [[ ":$PATH:" != *":$INSTALL_DIR:"* ]]; then
    echo ""
    echo "Note: Add $INSTALL_DIR to your PATH:"
    echo "  export PATH=\"\$PATH:$INSTALL_DIR\""
fi

echo ""
echo "Done! Run: source $SHELL_RC"
echo ""
echo "Usage: gpr"
