#!/bin/bash
set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WEB_DIR="$PROJECT_ROOT/web"
ELECTRON_DIR="$PROJECT_ROOT/electron"
RESOURCES_DIR="$ELECTRON_DIR/resources/server"

echo -e "${BLUE}Building Reliant Electron App (Cross-Platform)${NC}"
echo "Project root: $PROJECT_ROOT"

# Function to print step
print_step() {
    echo -e "\n${YELLOW}📦 $1${NC}"
}

# Function to check if command exists
command_exists() {
    command -v "$1" >/dev/null 2>&1
}

# Check prerequisites
print_step "Checking prerequisites..."

if ! command_exists go; then
    echo -e "${RED}❌ Go is not installed${NC}"
    exit 1
fi

if ! command_exists npm; then
    echo -e "${RED}❌ npm is not installed${NC}"
    exit 1
fi

if ! command_exists node; then
    echo -e "${RED}❌ Node.js is not installed${NC}"
    exit 1
fi

echo -e "${GREEN}✅ Prerequisites check passed${NC}"

# Clean and create resources directory
print_step "Setting up resources directory..."
rm -rf "$RESOURCES_DIR"
mkdir -p "$RESOURCES_DIR"/{mac-x64,mac-arm64,win32-amd64,win32-arm64,linux-amd64,linux-arm64}

# Build Go backend for all platforms
print_step "Building Go backend for all platforms..."

cd "$PROJECT_ROOT"

# macOS (AMD64)
echo "Building for macOS AMD64..."
GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o "$RESOURCES_DIR/mac-x64/reliant-backend" ./cmd/reliant/

# macOS (ARM64)
echo "Building for macOS ARM64..."
GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o "$RESOURCES_DIR/mac-arm64/reliant-backend" ./cmd/reliant/

# Windows (AMD64)
echo "Building for Windows AMD64..."
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o "$RESOURCES_DIR/win32-amd64/reliant-backend.exe" ./cmd/reliant/

# Windows (ARM64)
echo "Building for Windows ARM64..."
GOOS=windows GOARCH=arm64 go build -ldflags="-s -w" -o "$RESOURCES_DIR/win32-arm64/reliant-backend.exe" ./cmd/reliant/

# Linux (AMD64)
echo "Building for Linux AMD64..."
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o "$RESOURCES_DIR/linux-amd64/reliant-backend" ./cmd/reliant/

# Linux (ARM64)
echo "Building for Linux ARM64..."
GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o "$RESOURCES_DIR/linux-arm64/reliant-backend" ./cmd/reliant/

echo -e "${GREEN}✅ Go backend built for all platforms${NC}"

# Build web application
print_step "Building web application..."
cd "$WEB_DIR"

if [ ! -d "node_modules" ]; then
    echo "Installing web dependencies..."
    npm ci --legacy-peer-deps
fi

echo "Building web application..."
npm run build

if [ ! -d "dist" ]; then
    echo -e "${RED}❌ Failed to build web application${NC}"
    exit 1
fi

echo -e "${GREEN}✅ Web application built successfully${NC}"

# Build Electron application
print_step "Building Electron application..."
cd "$ELECTRON_DIR"

if [ ! -d "node_modules" ]; then
    echo "Installing Electron dependencies..."
    npm ci
fi

echo "Building Electron application..."
npm run build:electron

echo -e "${GREEN}✅ Electron application built successfully${NC}"

# List output files
print_step "Build completed!"
echo -e "${GREEN}Backend binaries:${NC}"
find "$RESOURCES_DIR" -type f -executable | sort

if [ -d "$ELECTRON_DIR/dist" ]; then
    echo -e "${GREEN}Electron packages:${NC}"
    find "$ELECTRON_DIR/dist" -name "*.dmg" -o -name "*.exe" -o -name "*.AppImage" -o -name "*.deb" | sort
fi

echo -e "\n${GREEN}🎉 Cross-platform build completed successfully!${NC}"
echo -e "Backend binaries: ${BLUE}$RESOURCES_DIR${NC}"
echo -e "Web build: ${BLUE}$WEB_DIR/dist${NC}"
if [ -d "$ELECTRON_DIR/dist" ]; then
    echo -e "Electron packages: ${BLUE}$ELECTRON_DIR/dist${NC}"
fi