#!/bin/bash
set -e

# Reliant Installation Script
# This script builds and installs the Reliant Electron app locally

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
ELECTRON_DIR="$PROJECT_ROOT/electron"
WEB_DIR="$PROJECT_ROOT/web"

# Print colored output
print_step() {
    echo -e "\n${BLUE}==>${NC} $1"
}

print_success() {
    echo -e "${GREEN}✓${NC} $1"
}

print_error() {
    echo -e "${RED}✗${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}⚠${NC} $1"
}

# Detect OS
detect_os() {
    case "$(uname -s)" in
        Darwin*)    OS="mac" ;;
        Linux*)     OS="linux" ;;
        MINGW*|MSYS*|CYGWIN*)     OS="win" ;;
        *)          
            print_error "Unsupported operating system: $(uname -s)"
            exit 1
            ;;
    esac
}

# Check prerequisites
check_prerequisites() {
    print_step "Checking prerequisites..."
    
    local missing=()
    
    # Check Node.js
    if ! command -v node &> /dev/null; then
        missing+=("Node.js")
    else
        local node_version=$(node --version | cut -d'v' -f2)
        print_success "Node.js $(node --version) found"
    fi
    
    # Check npm
    if ! command -v npm &> /dev/null; then
        missing+=("npm")
    else
        print_success "npm $(npm --version) found"
    fi
    
    # Check Go
    if ! command -v go &> /dev/null; then
        missing+=("Go")
    else
        print_success "Go $(go version | awk '{print $3}') found"
    fi
    
    # Check Git
    if ! command -v git &> /dev/null; then
        missing+=("Git")
    else
        print_success "Git $(git --version | awk '{print $3}') found"
    fi
    
    if [ ${#missing[@]} -gt 0 ]; then
        print_error "Missing required dependencies: ${missing[*]}"
        echo ""
        echo "Please install the missing dependencies:"
        for dep in "${missing[@]}"; do
            case $dep in
                "Node.js"|"npm")
                    echo "  - Node.js/npm: https://nodejs.org/"
                    ;;
                "Go")
                    echo "  - Go: https://golang.org/dl/"
                    ;;
                "Git")
                    echo "  - Git: https://git-scm.com/downloads"
                    ;;
            esac
        done
        exit 1
    fi
}

# Install npm dependencies
install_dependencies() {
    print_step "Installing dependencies..."
    
    # Install web dependencies
    if [ ! -d "$WEB_DIR/node_modules" ]; then
        print_step "Installing web dependencies..."
        cd "$WEB_DIR"
        npm ci || npm install
        print_success "Web dependencies installed"
    else
        print_success "Web dependencies already installed"
    fi
    
    # Install electron dependencies
    if [ ! -d "$ELECTRON_DIR/node_modules" ]; then
        print_step "Installing Electron dependencies..."
        cd "$ELECTRON_DIR"
        npm ci || npm install
        print_success "Electron dependencies installed"
    else
        print_success "Electron dependencies already installed"
    fi
}

# Build backend
build_backend() {
    print_step "Building backend..."
    cd "$PROJECT_ROOT"
    
    # Build development backend
    go build -o dist/reliant ./cmd/reliant/
    if [ ! -f "dist/reliant" ]; then
        print_error "Failed to build backend"
        exit 1
    fi
    print_success "Backend built successfully"
}

# Build web app
build_web() {
    print_step "Building web application..."
    cd "$WEB_DIR"
    
    npm run build
    if [ ! -d "dist" ]; then
        print_error "Failed to build web application"
        exit 1
    fi
    print_success "Web application built successfully"
}

# Build and package Electron app
build_electron() {
    print_step "Building Electron application for $OS..."
    cd "$ELECTRON_DIR"
    
    case $OS in
        mac)
            npm run dist:mac
            ;;
        linux)
            npm run dist:linux
            ;;
        win)
            npm run dist:win
            ;;
    esac
    
    if [ $? -ne 0 ]; then
        print_error "Failed to build Electron application"
        exit 1
    fi
    print_success "Electron application built successfully"
}

# Install the application
install_app() {
    print_step "Installing Reliant..."
    
    case $OS in
        mac)
            # Find the built app
            local APP_PATH="$ELECTRON_DIR/dist/mac-arm64/Reliant.app"
            if [ ! -d "$APP_PATH" ]; then
                APP_PATH="$ELECTRON_DIR/dist/mac/Reliant.app"
            fi
            
            if [ ! -d "$APP_PATH" ]; then
                print_error "Built application not found"
                exit 1
            fi
            
            # Copy to Applications
            print_step "Installing to /Applications..."
            rm -rf /Applications/Reliant.app 2>/dev/null || true
            cp -R "$APP_PATH" /Applications/
            print_success "Reliant installed to /Applications"
            
            # Create command line symlink
            if [ -d "/usr/local/bin" ]; then
                print_step "Creating command line launcher..."
                cat > /tmp/reliant-launcher << 'EOF'
#!/bin/bash
open -a Reliant "$@"
EOF
                chmod +x /tmp/reliant-launcher
                sudo mv /tmp/reliant-launcher /usr/local/bin/reliant 2>/dev/null || {
                    print_warning "Could not install command line launcher (needs sudo)"
                }
                [ -f "/usr/local/bin/reliant" ] && print_success "Command line launcher installed"
            fi
            ;;
            
        linux)
            # Find the built package
            local DEB_PATH=$(find "$ELECTRON_DIR/dist" -name "*.deb" | head -1)
            local RPM_PATH=$(find "$ELECTRON_DIR/dist" -name "*.rpm" | head -1)
            local APPIMAGE_PATH=$(find "$ELECTRON_DIR/dist" -name "*.AppImage" | head -1)
            
            if [ -f "$DEB_PATH" ] && command -v dpkg &> /dev/null; then
                print_step "Installing .deb package..."
                sudo dpkg -i "$DEB_PATH"
                print_success "Reliant installed via dpkg"
            elif [ -f "$RPM_PATH" ] && command -v rpm &> /dev/null; then
                print_step "Installing .rpm package..."
                sudo rpm -i "$RPM_PATH"
                print_success "Reliant installed via rpm"
            elif [ -f "$APPIMAGE_PATH" ]; then
                print_step "Installing AppImage..."
                mkdir -p ~/Applications
                cp "$APPIMAGE_PATH" ~/Applications/Reliant.AppImage
                chmod +x ~/Applications/Reliant.AppImage
                print_success "Reliant AppImage installed to ~/Applications"
            else
                print_error "No suitable package found for installation"
                exit 1
            fi
            ;;
            
        win)
            local EXE_PATH=$(find "$ELECTRON_DIR/dist" -name "*.exe" | head -1)
            if [ -f "$EXE_PATH" ]; then
                print_step "Installer created at: $EXE_PATH"
                print_warning "Please run the installer manually: $EXE_PATH"
            else
                print_error "Windows installer not found"
                exit 1
            fi
            ;;
    esac
}

# Main installation flow
main() {
    echo -e "${BLUE}╔══════════════════════════════════╗${NC}"
    echo -e "${BLUE}║   Reliant Installation Script    ║${NC}"
    echo -e "${BLUE}╚══════════════════════════════════╝${NC}"
    
    detect_os
    echo -e "Detected OS: ${GREEN}$OS${NC}"
    
    check_prerequisites
    
    # Ask for confirmation
    echo ""
    echo -e "${YELLOW}This will:${NC}"
    echo "  1. Install npm dependencies"
    echo "  2. Build the backend (Go)"
    echo "  3. Build the web application"
    echo "  4. Package the Electron app"
    if [ "$OS" = "mac" ]; then
        echo "  5. Install to /Applications"
    elif [ "$OS" = "linux" ]; then
        echo "  5. Install the application package"
    fi
    echo ""
    read -p "Continue? (y/n) " -n 1 -r
    echo ""
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        echo "Installation cancelled"
        exit 0
    fi
    
    # Run installation steps
    install_dependencies
    build_backend
    build_web
    build_electron
    install_app
    
    # Run post-installation initialization
    print_step "Running post-installation setup..."
    if [ -f "$SCRIPT_DIR/post-install-init.sh" ]; then
        bash "$SCRIPT_DIR/post-install-init.sh"
    else
        print_warning "Post-installation script not found, skipping tool installation"
    fi
    
    # Success message
    echo ""
    echo -e "${GREEN}╔══════════════════════════════════╗${NC}"
    echo -e "${GREEN}║   Installation Successful! 🎉    ║${NC}"
    echo -e "${GREEN}╚══════════════════════════════════╝${NC}"
    echo ""
    
    case $OS in
        mac)
            echo "Reliant has been installed to /Applications"
            echo ""
            echo "You can now:"
            echo "  • Launch from Applications folder"
            echo "  • Launch from Spotlight (Cmd+Space, type 'Reliant')"
            [ -f "/usr/local/bin/reliant" ] && echo "  • Launch from terminal: reliant"
            ;;
        linux)
            echo "Reliant has been installed"
            echo "Launch it from your application menu or terminal"
            ;;
        win)
            echo "Please run the installer to complete installation"
            ;;
    esac
}

# Run main function
main "$@"