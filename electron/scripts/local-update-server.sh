#!/bin/bash
# Local Update Server for Testing
# This script sets up and starts a local HTTP server for testing app updates.
#
# Usage:
#   ./local-update-server.sh [port]
#
# Default port is 8080

set -e

PORT="${1:-8080}"
SERVER_DIR="${HOME}/.reliant-update-test-server"

echo "=========================================="
echo "Reliant Local Update Test Server"
echo "=========================================="
echo ""

# Create server directory if it doesn't exist
if [ ! -d "$SERVER_DIR" ]; then
    echo "Creating test server directory: $SERVER_DIR"
    mkdir -p "$SERVER_DIR"
fi

# Check if there are any files to serve
ZIP_COUNT=$(find "$SERVER_DIR" -name "*.zip" 2>/dev/null | wc -l | tr -d ' ')
YML_COUNT=$(find "$SERVER_DIR" -name "latest-mac*.yml" -o -name "alpha-mac*.yml" 2>/dev/null | wc -l | tr -d ' ')

if [ "$ZIP_COUNT" -eq 0 ] || [ "$YML_COUNT" -eq 0 ]; then
    echo ""
    echo "WARNING: Server directory is missing required files!"
    echo ""
    echo "To test updates, you need to copy the following files to:"
    echo "  $SERVER_DIR"
    echo ""
    echo "Required files:"
    echo "  1. The new version's .zip file (e.g., Reliant-0.2.6-rc1-mac-arm64.zip)"
    echo "  2. The latest-mac.yml or alpha-mac.yml file"
    echo ""
    echo "These files are generated when you build the app:"
    echo "  npm run dist:mac:dev"
    echo ""
    echo "The build output is in: electron/dist/"
    echo ""
fi

echo "Server directory contents:"
echo "--------------------------"
ls -la "$SERVER_DIR" 2>/dev/null || echo "(empty)"
echo ""

# Check if Python is available
if ! command -v python3 &> /dev/null; then
    echo "ERROR: python3 is required but not installed."
    exit 1
fi

echo "Starting HTTP server on http://localhost:$PORT"
echo ""
echo "To use this server, run your OLD version of the app with:"
echo ""
echo "  open --env RELIANT_UPDATE_URL=http://localhost:$PORT/ /path/to/old/Reliant.app"
echo ""
echo "Or for the app in dist/mac-arm64:"
echo ""
echo "  open --env RELIANT_UPDATE_URL=http://localhost:$PORT/ electron/dist/mac-arm64/Reliant.app"
echo ""
echo "Press Ctrl+C to stop the server."
echo "=========================================="
echo ""

cd "$SERVER_DIR"
python3 -m http.server "$PORT"
