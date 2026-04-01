#!/bin/bash
set -e

# Quick install script for Reliant (development/testing)
# This is a faster version that skips some checks

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

echo "🚀 Quick Installing Reliant..."

# Ensure web dependencies are installed (needed for protoc-gen-es)
cd "$PROJECT_ROOT"
if [ ! -d "web/node_modules" ]; then
    echo "📦 Installing web dependencies..."
    cd web && npm install && cd "$PROJECT_ROOT"
fi

# Ensure electron dependencies are installed
if [ ! -d "electron/node_modules" ]; then
    echo "📦 Installing electron dependencies..."
    cd electron && npm install && cd "$PROJECT_ROOT"
fi

# Generate Go code (DB schema + sqlc for both drivers, then docgen)
echo "📦 Regenerating DB schema and sqlc code (SQLite + Postgres)..."
(cd "$PROJECT_ROOT" && make db-regenerate)

echo "📦 Generating Go docs/reference code..."
go run tools/docgen/celref/main.go internal/workflow/v3 internal/workflow/v3/reference/cel_reference.go
go run tools/docgen/refcheck/main.go
go run tools/docgen/shortcuts/main.go config/shortcuts.yaml web/src/store/shortcutsData.generated.ts generated/docs-source/settings/keyboard-shortcuts.generated.md

# Generate protobuf code
echo "📦 Generating protobuf code..."
PATH="$PROJECT_ROOT/web/node_modules/.bin:$PATH" buf generate

# Build V2 backend
echo "📦 Building V2 backend..."
go build -buildvcs=false -o dist/reliant ./cmd/reliant/

# Build web (if needed)
if [ ! -d "web/dist" ] || [ "$1" = "--rebuild" ]; then
    echo "📦 Building web app..."
    cd web && npm run build:alpha
else
    echo "⏭️  Skipping web build (already exists)"
fi

# Build V2 platform binaries
echo "📦 Building V2 platform binaries..."
cd "$PROJECT_ROOT"
if [[ "$OSTYPE" == "darwin"* ]]; then
    ./scripts/build-electron.sh mac
elif [[ "$OSTYPE" == "linux-gnu"* ]]; then
    ./scripts/build-electron.sh linux
else
    ./scripts/build-electron.sh win
fi

# Build and install Electron app
echo "📦 Building Electron app..."
cd "$PROJECT_ROOT/electron"

# Quick build for current platform only
if [[ "$OSTYPE" == "darwin"* ]]; then
    npm run dist:mac:dev
    echo "📥 Installing to /Applications..."
    rm -rf /Applications/Reliant.app 2>/dev/null || true
    # Use cp -R (capital R) to preserve symlinks in framework bundles
    cp -R dist/mac-arm64/Reliant.app /Applications/ 2>/dev/null || \
    cp -R dist/mac/Reliant.app /Applications/
    echo "✅ Installed to /Applications/Reliant.app"
elif [[ "$OSTYPE" == "linux-gnu"* ]]; then
    npm run dist:linux
    echo "✅ Check electron/dist/ for the Linux package"
else
    npm run dist:win
    echo "✅ Check electron/dist/ for the Windows installer"
fi

echo ""
echo "🎉 Quick install complete!"