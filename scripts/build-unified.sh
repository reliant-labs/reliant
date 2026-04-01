#!/bin/bash
set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
PURPLE='\033[0;35m'
NC='\033[0m' # No Color

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WEB_DIR="$PROJECT_ROOT/web"
ELECTRON_DIR="$PROJECT_ROOT/electron"
DIST_DIR="$PROJECT_ROOT/dist"
RESOURCES_DIR="$ELECTRON_DIR/resources"

# Default platform is current system
PLATFORM=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

# Map architecture names
case $ARCH in
    x86_64) ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    i386|i686) ARCH="386" ;;
esac

# Command line options
BUILD_ALL_PLATFORMS=false
SKIP_WEB_BUILD=false
SKIP_BACKEND_BUILD=false
VERBOSE=false
CLEAN=false

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --all-platforms)
            BUILD_ALL_PLATFORMS=true
            shift
            ;;
        --platform)
            PLATFORM="$2"
            shift 2
            ;;
        --arch)
            ARCH="$2"
            shift 2
            ;;
        --skip-web)
            SKIP_WEB_BUILD=true
            shift
            ;;
        --skip-backend)
            SKIP_BACKEND_BUILD=true
            shift
            ;;
        --verbose)
            VERBOSE=true
            shift
            ;;
        --clean)
            CLEAN=true
            shift
            ;;
        --help)
            echo "Usage: $0 [options]"
            echo "Options:"
            echo "  --all-platforms     Build for all supported platforms"
            echo "  --platform <name>   Build for specific platform (linux, darwin, windows)"
            echo "  --arch <name>       Build for specific architecture (amd64, arm64, 386)"
            echo "  --skip-web          Skip web application build"
            echo "  --skip-backend      Skip backend build"
            echo "  --verbose           Enable verbose output"
            echo "  --clean             Clean all build artifacts before building"
            echo "  --help              Show this help message"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

echo -e "${BLUE}🚀 Reliant Unified Build System${NC}"
echo -e "${PURPLE}Building single-entry-point desktop application${NC}"
echo "Project root: $PROJECT_ROOT"
echo "Target platform: $PLATFORM"
echo "Target architecture: $ARCH"

# Function to print step
print_step() {
    echo -e "\n${YELLOW}📦 $1${NC}"
}

# Function to print success
print_success() {
    echo -e "${GREEN}✅ $1${NC}"
}

# Function to print error
print_error() {
    echo -e "${RED}❌ $1${NC}"
}

# Function to check if command exists
command_exists() {
    command -v "$1" >/dev/null 2>&1
}

# Function to run command with optional verbose output
run_cmd() {
    if [[ "$VERBOSE" == "true" ]]; then
        "$@"
    else
        "$@" > /dev/null 2>&1
    fi
}

# Check prerequisites
print_step "Checking prerequisites..."

MISSING_DEPS=()

if ! command_exists go; then
    MISSING_DEPS+=("Go")
fi

if ! command_exists npm; then
    MISSING_DEPS+=("npm")
fi

if ! command_exists node; then
    MISSING_DEPS+=("Node.js")
fi

if [ ${#MISSING_DEPS[@]} -ne 0 ]; then
    print_error "Missing dependencies: ${MISSING_DEPS[*]}"
    echo "Please install the missing dependencies and try again."
    exit 1
fi

print_success "Prerequisites check passed"

# Clean build artifacts if requested
if [[ "$CLEAN" == "true" ]]; then
    print_step "Cleaning build artifacts..."
    rm -rf "$DIST_DIR"
    rm -rf "$WEB_DIR/dist"
    rm -rf "$ELECTRON_DIR/dist"
    rm -rf "$RESOURCES_DIR"
    print_success "Build artifacts cleaned"
fi

# Create necessary directories
print_step "Creating build directories..."
mkdir -p "$DIST_DIR"
mkdir -p "$RESOURCES_DIR/bin"
mkdir -p "$ELECTRON_DIR/build"

# Install web dependencies early (needed for protoc-gen-es used by buf generate)
print_step "Installing web dependencies..."
cd "$WEB_DIR"
if [ ! -d "node_modules" ]; then
    echo "Installing web dependencies..."
    run_cmd npm ci --legacy-peer-deps
fi

# Generate protobuf code
print_step "Generating protobuf code..."
cd "$PROJECT_ROOT"
if command_exists buf; then
    # Add web/node_modules/.bin to PATH for protoc-gen-es
    export PATH="$WEB_DIR/node_modules/.bin:$PATH"
    if [[ "$VERBOSE" == "true" ]]; then
        buf generate
    else
        buf generate > /dev/null 2>&1
    fi
    print_success "Protobuf code generated"
else
    print_error "buf not installed - proto generation skipped"
    echo "Install buf: https://buf.build/docs/installation"
    exit 1
fi

# Function to build Go backend for specific platform
build_backend_for_platform() {
    local target_os="$1"
    local target_arch="$2"
    local binary_name="reliant-backend"
    
    if [[ "$target_os" == "windows" ]]; then
        binary_name="${binary_name}.exe"
    fi
    
    local output_path="$RESOURCES_DIR/bin/${target_os}-${target_arch}/$binary_name"
    mkdir -p "$(dirname "$output_path")"
    
    echo "Building backend for $target_os/$target_arch..."
    
    cd "$PROJECT_ROOT"
    GOOS="$target_os" GOARCH="$target_arch" CGO_ENABLED=0 go build \
        -ldflags="-s -w -X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo 'dev')" \
        -o "$output_path" \
        ./cmd/reliant/
    
    if [ ! -f "$output_path" ]; then
        print_error "Failed to build backend for $target_os/$target_arch"
        return 1
    fi
    
    # Make executable on Unix systems
    if [[ "$target_os" != "windows" ]]; then
        chmod +x "$output_path"
    fi
    
    echo "Backend built: $output_path"
}

# Build Go backend
if [[ "$SKIP_BACKEND_BUILD" != "true" ]]; then
    print_step "Building Go backend..."
    
    if [[ "$BUILD_ALL_PLATFORMS" == "true" ]]; then
        # Build for all major platforms
        build_backend_for_platform "linux" "amd64"
        build_backend_for_platform "linux" "arm64"
        build_backend_for_platform "darwin" "amd64"
        build_backend_for_platform "darwin" "arm64"
        build_backend_for_platform "windows" "amd64"
        build_backend_for_platform "windows" "arm64"
    else
        # Build for current platform only
        build_backend_for_platform "$PLATFORM" "$ARCH"
    fi
    
    print_success "Go backend built successfully"
else
    echo "Skipping backend build"
fi

# Build web application
if [[ "$SKIP_WEB_BUILD" != "true" ]]; then
    print_step "Building web application..."
    cd "$WEB_DIR"
    
    if [ ! -d "node_modules" ]; then
        echo "Installing web dependencies..."
        run_cmd npm ci --legacy-peer-deps
    fi
    
    echo "Building web application..."
    run_cmd npm run build
    
    if [ ! -d "dist" ]; then
        print_error "Failed to build web application"
        exit 1
    fi
    
    print_success "Web application built successfully"
else
    echo "Skipping web build"
fi

# Install Electron dependencies
print_step "Installing Electron dependencies..."
cd "$ELECTRON_DIR"

if [ ! -d "node_modules" ]; then
    echo "Installing Electron dependencies..."
    run_cmd npm ci
fi

# Create app icons if they don't exist
print_step "Setting up application assets..."

# Create a basic PNG icon if it doesn't exist
if [ ! -f "$ELECTRON_DIR/build/icon.png" ]; then
    echo "Creating default application icon..."
    # Create a simple colored square as placeholder
    cat > "$ELECTRON_DIR/build/icon.png.base64" << 'EOF'
iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==
EOF
    # This creates a minimal 1x1 transparent PNG - replace with actual icon
    base64 -d "$ELECTRON_DIR/build/icon.png.base64" > "$ELECTRON_DIR/build/icon.png" 2>/dev/null || \
    echo "Note: Please add a proper icon.png file to electron/build/"
fi

# Copy icon for other formats
if [ ! -f "$ELECTRON_DIR/build/icon.ico" ] && [ -f "$ELECTRON_DIR/build/icon.png" ]; then
    cp "$ELECTRON_DIR/build/icon.png" "$ELECTRON_DIR/build/icon.ico"
fi

if [ ! -f "$ELECTRON_DIR/build/icon.icns" ] && [ -f "$ELECTRON_DIR/build/icon.png" ]; then
    cp "$ELECTRON_DIR/build/icon.png" "$ELECTRON_DIR/build/icon.icns"
fi

# Build Electron application
print_step "Building Electron application..."

# Set environment for production build
export NODE_ENV=production

if [[ "$BUILD_ALL_PLATFORMS" == "true" ]]; then
    echo "Building for all platforms..."
    run_cmd npm run dist -- --publish=never --mac --win --linux
else
    echo "Building for current platform..."
    case "$PLATFORM" in
        darwin)
            run_cmd npm run dist -- --publish=never --mac
            ;;
        linux)
            run_cmd npm run dist -- --publish=never --linux
            ;;
        windows)
            run_cmd npm run dist -- --publish=never --win
            ;;
        *)
            echo "Building for default platform..."
            run_cmd npm run dist -- --publish=never
            ;;
    esac
fi

print_success "Electron application built successfully"

# List output files
print_step "Build completed! 🎉"
echo -e "${GREEN}Output files:${NC}"

if [ -d "$ELECTRON_DIR/dist" ]; then
    echo -e "${BLUE}Electron packages:${NC}"
    find "$ELECTRON_DIR/dist" -name "*.dmg" -o -name "*.exe" -o -name "*.AppImage" -o -name "*.deb" | while read -r file; do
        size=$(du -h "$file" | cut -f1)
        echo "  📦 $file ($size)"
    done
fi

echo -e "${BLUE}Backend binaries:${NC}"
find "$RESOURCES_DIR/bin" -type f | while read -r file; do
    size=$(du -h "$file" | cut -f1)
    echo "  🔧 $file ($size)"
done

echo -e "${BLUE}Web application:${NC}"
if [ -d "$WEB_DIR/dist" ]; then
    size=$(du -sh "$WEB_DIR/dist" | cut -f1)
    echo "  🌐 $WEB_DIR/dist ($size)"
fi

print_step "Installation Instructions"
echo -e "${YELLOW}For end users:${NC}"
echo "1. Download the appropriate package for your platform from electron/dist/"
echo "2. Install/run the package - no additional setup required!"
echo "3. The app will automatically start the backend and web interface"

echo -e "\n${YELLOW}Distribution files:${NC}"
case "$PLATFORM" in
    darwin)
        echo "  macOS: .dmg file for drag-and-drop installation"
        ;;
    linux)
        echo "  Linux: .AppImage for portable execution"
        ;;
    windows)
        echo "  Windows: .exe installer with NSIS"
        ;;
esac

print_success "Build process completed successfully!"
echo -e "${GREEN}🚀 Your single-entry-point Reliant desktop app is ready for distribution!${NC}"