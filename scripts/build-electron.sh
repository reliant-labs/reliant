#!/bin/bash
set -e

# Build script for Electron app backend binaries
# Usage: ./scripts/build-electron.sh [mac|win|linux|all] [--parallel]
#
# Options:
#   --parallel    Build architectures in parallel (default for CI)
#   --sequential  Build architectures sequentially (default for local)
#
# Environment:
#   VERSION       Override version string (default: from electron/package.json)
#   PARALLEL      Set to "true" to enable parallel builds

PLATFORM=${1:-all}
PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Detect if we should build in parallel
# Default: parallel in CI, sequential locally (to avoid CPU contention during dev)
if [[ "$2" == "--parallel" ]] || [[ "$PARALLEL" == "true" ]] || [[ -n "$CI" ]]; then
    PARALLEL_BUILD=true
else
    PARALLEL_BUILD=false
fi

echo "Building backend binaries for platform: $PLATFORM"
echo "Project root: $PROJECT_ROOT"
echo "Parallel build: $PARALLEL_BUILD"

cd "$PROJECT_ROOT"

# Create resources directory structure
mkdir -p electron/resources/server

# Get version from environment or electron package.json
get_version() {
    if [[ -n "$VERSION" ]]; then
        echo "$VERSION"
    else
        node -p "require('./electron/package.json').version"
    fi
}

LDFLAGS_BASE="-s -w -X github.com/reliant-labs/reliant/internal/build.BuildType=production"

build_binary() {
    local goos=$1
    local goarch=$2
    local output_dir=$3
    local version=$4
    local ext=""
    
    [[ "$goos" == "windows" ]] && ext=".exe"
    
    echo "  Building for $goos/$goarch..."
    mkdir -p "$output_dir"

    GOOS=$goos GOARCH=$goarch go build \
        -ldflags="$LDFLAGS_BASE -X github.com/reliant-labs/reliant/internal/version.Version=$version" \
        -o "$output_dir/reliant-backend$ext" \
        ./cmd/reliant/
}

build_mac() {
    echo "Building macOS binaries..."
    local version=$(get_version)
    echo "  Version: $version"

    if [[ "$PARALLEL_BUILD" == "true" ]]; then
        build_binary darwin arm64 "electron/resources/server/mac-arm64" "$version" &
        local pid1=$!
        build_binary darwin amd64 "electron/resources/server/mac-x64" "$version" &
        local pid2=$!
        
        wait $pid1 && echo "  ARM64 complete"
        wait $pid2 && echo "  AMD64 complete"
    else
        build_binary darwin arm64 "electron/resources/server/mac-arm64" "$version"
        build_binary darwin amd64 "electron/resources/server/mac-x64" "$version"
    fi

    echo "macOS binaries built successfully"
}

build_win() {
    echo "Building Windows binaries..."
    local version=$(get_version)
    echo "  Version: $version"

    if [[ "$PARALLEL_BUILD" == "true" ]]; then
        build_binary windows amd64 "electron/resources/server/win32-amd64" "$version" &
        local pid1=$!
        build_binary windows arm64 "electron/resources/server/win32-arm64" "$version" &
        local pid2=$!
        
        wait $pid1 && echo "  AMD64 complete"
        wait $pid2 && echo "  ARM64 complete"
    else
        build_binary windows amd64 "electron/resources/server/win32-amd64" "$version"
        build_binary windows arm64 "electron/resources/server/win32-arm64" "$version"
    fi

    echo "Windows binaries built successfully"
}

build_linux() {
    echo "Building Linux binaries..."
    local version=$(get_version)
    echo "  Version: $version"

    if [[ "$PARALLEL_BUILD" == "true" ]]; then
        build_binary linux amd64 "electron/resources/server/linux-amd64" "$version" &
        local pid1=$!
        build_binary linux arm64 "electron/resources/server/linux-arm64" "$version" &
        local pid2=$!
        
        wait $pid1 && echo "  AMD64 complete"
        wait $pid2 && echo "  ARM64 complete"
    else
        build_binary linux amd64 "electron/resources/server/linux-amd64" "$version"
        build_binary linux arm64 "electron/resources/server/linux-arm64" "$version"
    fi

    echo "Linux binaries built successfully"
}

build_all() {
    echo "Building for all platforms..."
    
    if [[ "$PARALLEL_BUILD" == "true" ]]; then
        # Build all platforms in parallel (6 binaries total)
        local version=$(get_version)
        echo "  Version: $version"
        
        build_binary darwin arm64 "electron/resources/server/mac-arm64" "$version" &
        local pids=($!)
        build_binary darwin amd64 "electron/resources/server/mac-x64" "$version" &
        pids+=($!)
        build_binary windows amd64 "electron/resources/server/win32-amd64" "$version" &
        pids+=($!)
        build_binary windows arm64 "electron/resources/server/win32-arm64" "$version" &
        pids+=($!)
        build_binary linux amd64 "electron/resources/server/linux-amd64" "$version" &
        pids+=($!)
        build_binary linux arm64 "electron/resources/server/linux-arm64" "$version" &
        pids+=($!)
        
        # Wait for all builds
        local failed=0
        for pid in "${pids[@]}"; do
            if ! wait $pid; then
                failed=$((failed + 1))
            fi
        done
        
        if [[ $failed -gt 0 ]]; then
            echo "ERROR: $failed builds failed"
            exit 1
        fi
    else
        build_mac
        build_win
        build_linux
    fi
    
    echo "All platform binaries built successfully"
}

# Make binaries executable on Unix systems
make_executable() {
    if [[ "$OSTYPE" != "msys" && "$OSTYPE" != "win32" ]]; then
        echo "Making binaries executable..."
        find electron/resources/server -name "reliant-*" -type f ! -name "*.exe" -exec chmod +x {} \;
    fi
}

# Main build logic
case "$PLATFORM" in
    mac)
        build_mac
        ;;
    win)
        build_win
        ;;
    linux)
        build_linux
        ;;
    all)
        build_all
        ;;
    *)
        echo "ERROR: Unknown platform: $PLATFORM"
        echo "Usage: $0 [mac|win|linux|all] [--parallel]"
        exit 1
        ;;
esac

# Make binaries executable
make_executable

echo "Build completed successfully!"
echo "Binaries are located in: electron/resources/server/"
ls -la electron/resources/server/