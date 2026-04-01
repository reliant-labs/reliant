#!/bin/bash

# Reliant CLI Installation Script
# This script installs the 'reliant' command to /usr/local/bin

set -e

echo "Installing Reliant CLI command..."

# Detect the operating system
OS="$(uname -s)"

case "$OS" in
    Darwin)
        # macOS
        APP_PATH="/Applications/Reliant.app"
        CLI_SOURCE="$APP_PATH/Contents/Resources/cli/reliant"
        
        if [ ! -d "$APP_PATH" ]; then
            echo "Error: Reliant.app not found in /Applications"
            echo "Please install Reliant first"
            exit 1
        fi
        
        if [ ! -f "$CLI_SOURCE" ]; then
            echo "Error: CLI script not found in Reliant.app"
            exit 1
        fi
        ;;
        
    Linux)
        # Linux - try to find the installation
        if [ -f "/opt/Reliant/resources/cli/reliant" ]; then
            CLI_SOURCE="/opt/Reliant/resources/cli/reliant"
        elif [ -f "/usr/lib/reliant/resources/cli/reliant" ]; then
            CLI_SOURCE="/usr/lib/reliant/resources/cli/reliant"
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
    echo "You can now use the following commands:"
    echo "  reliant              # Open Reliant in current directory"
    echo "  reliant /path/to/dir # Open Reliant in specified directory"
else
    echo "⚠ Installation completed but 'reliant' command not found in PATH"
    echo "You may need to add /usr/local/bin to your PATH or restart your terminal"
fi