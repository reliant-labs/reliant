#!/bin/bash

# Reliant CLI Installation Script
# This script installs the 'reliant' command to /usr/local/bin
# by symlinking the bundled Go backend binary.

set -e

echo "Installing Reliant CLI command..."

# Detect the operating system and architecture
OS="$(uname -s)"
ARCH="$(uname -m)"

case "$OS" in
    Darwin)
        APP_PATH="/Applications/Reliant.app"
        if [ "$ARCH" = "arm64" ]; then
            CLI_SOURCE="$APP_PATH/Contents/Resources/server/mac-arm64/reliant-backend"
        else
            CLI_SOURCE="$APP_PATH/Contents/Resources/server/mac-x64/reliant-backend"
        fi
        
        if [ ! -d "$APP_PATH" ]; then
            echo "Error: Reliant.app not found in /Applications"
            echo "Please install Reliant first"
            exit 1
        fi
        
        if [ ! -f "$CLI_SOURCE" ]; then
            echo "Error: Go binary not found at $CLI_SOURCE"
            exit 1
        fi
        ;;
        
    Linux)
        if [ "$ARCH" = "aarch64" ] || [ "$ARCH" = "arm64" ]; then
            PLATFORM_DIR="linux-arm64"
        else
            PLATFORM_DIR="linux-amd64"
        fi

        if [ -f "/opt/Reliant/resources/server/$PLATFORM_DIR/reliant-backend" ]; then
            CLI_SOURCE="/opt/Reliant/resources/server/$PLATFORM_DIR/reliant-backend"
        elif [ -f "/usr/lib/reliant/resources/server/$PLATFORM_DIR/reliant-backend" ]; then
            CLI_SOURCE="/usr/lib/reliant/resources/server/$PLATFORM_DIR/reliant-backend"
        else
            echo "Error: Could not find Reliant installation"
            echo "Please install Reliant first"
            exit 1
        fi
        ;;
        
    *)
        echo "Error: Unsupported operating system: $OS"
        exit 1
        ;;
esac

# Target location
CLI_TARGET="/usr/local/bin/reliant"

# Create /usr/local/bin if it doesn't exist
if [ ! -d "/usr/local/bin" ]; then
    echo "Creating /usr/local/bin directory..."
    sudo mkdir -p /usr/local/bin
fi

# Remove existing symlink if it exists
if [ -L "$CLI_TARGET" ] || [ -f "$CLI_TARGET" ]; then
    echo "Removing existing reliant command..."
    sudo rm -f "$CLI_TARGET"
fi

# Create symlink
echo "Creating symlink from $CLI_SOURCE to $CLI_TARGET..."
sudo ln -sf "$CLI_SOURCE" "$CLI_TARGET"

# Make sure it's executable
sudo chmod +x "$CLI_TARGET"

# Verify installation
if command -v reliant &> /dev/null; then
    echo "✓ Successfully installed 'reliant' command"
    echo ""
    echo "Usage: reliant --help"
else
    echo "⚠ Installation completed but 'reliant' command not found in PATH"
    echo "You may need to add /usr/local/bin to your PATH or restart your terminal"
fi
