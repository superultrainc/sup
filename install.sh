#!/bin/bash
set -e

# Install gpr - GitHub PR picker for superultrainc

INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

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

# Shell function to add
SHELL_FUNC='
# gpr - GitHub PR picker
gco() {
    gpr
    if [[ -f ~/.gpr-selection ]]; then
        local repo branch
        repo=$(cat ~/.gpr-selection | jq -r .repo)
        branch=$(cat ~/.gpr-selection | jq -r .branch)
        if [[ -n "$repo" && -n "$branch" ]]; then
            local repo_path="$HOME/Development/$repo"
            if [[ -d "$repo_path" ]]; then
                cd "$repo_path" && git fetch origin && git checkout "$branch"
            else
                echo "Repo not found at $repo_path"
            fi
        fi
        rm -f ~/.gpr-selection
    fi
}'

# Check if already installed
if grep -q "gco()" "$SHELL_RC" 2>/dev/null; then
    echo "Shell function already exists in $SHELL_RC"
else
    echo "Adding shell function to $SHELL_RC..."
    echo "$SHELL_FUNC" >> "$SHELL_RC"
fi

# Ensure install dir is in PATH
if [[ ":$PATH:" != *":$INSTALL_DIR:"* ]]; then
    echo ""
    echo "Add $INSTALL_DIR to your PATH:"
    echo "  export PATH=\"\$PATH:$INSTALL_DIR\""
fi

echo ""
echo "Done! Restart your shell or run: source $SHELL_RC"
echo ""
echo "Usage:"
echo "  gpr  - Open PR picker"
echo "  gco  - Pick a PR and checkout the branch"
