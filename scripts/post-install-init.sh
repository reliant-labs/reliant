#!/bin/bash
set -e

# Post-Installation Initialization Script for Reliant
# This script ensures required tools (ripgrep, fzf, git, gh) are installed

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

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

# Detect OS and package manager
detect_system() {
    case "$(uname -s)" in
        Darwin*)
            OS="mac"
            # Check for Homebrew
            if command -v brew &> /dev/null; then
                PKG_MANAGER="brew"
                PKG_INSTALL="brew install"
            else
                print_warning "Homebrew not found. Please install Homebrew first: https://brew.sh"
                exit 1
            fi
            ;;
        Linux*)
            OS="linux"
            # Detect Linux package manager
            if command -v apt-get &> /dev/null; then
                PKG_MANAGER="apt"
                PKG_INSTALL="sudo apt-get install -y"
            elif command -v dnf &> /dev/null; then
                PKG_MANAGER="dnf"
                PKG_INSTALL="sudo dnf install -y"
            elif command -v yum &> /dev/null; then
                PKG_MANAGER="yum"
                PKG_INSTALL="sudo yum install -y"
            elif command -v pacman &> /dev/null; then
                PKG_MANAGER="pacman"
                PKG_INSTALL="sudo pacman -S --noconfirm"
            elif command -v zypper &> /dev/null; then
                PKG_MANAGER="zypper"
                PKG_INSTALL="sudo zypper install -y"
            else
                print_error "No supported package manager found"
                exit 1
            fi
            ;;
        MINGW*|MSYS*|CYGWIN*)
            OS="win"
            # Check for Chocolatey or Scoop
            if command -v choco &> /dev/null; then
                PKG_MANAGER="choco"
                PKG_INSTALL="choco install -y"
            elif command -v scoop &> /dev/null; then
                PKG_MANAGER="scoop"
                PKG_INSTALL="scoop install"
            else
                print_warning "Please install Chocolatey (https://chocolatey.org) or Scoop (https://scoop.sh)"
                exit 1
            fi
            ;;
        *)
            print_error "Unsupported operating system: $(uname -s)"
            exit 1
            ;;
    esac
}

# Check if a command exists
command_exists() {
    command -v "$1" &> /dev/null
}

# Install ripgrep
install_ripgrep() {
    if command_exists rg; then
        print_success "ripgrep (rg) is already installed ($(rg --version | head -1))"
        return 0
    fi
    
    print_step "Installing ripgrep..."
    
    case $PKG_MANAGER in
        brew)
            $PKG_INSTALL ripgrep
            ;;
        apt)
            # Try to install ripgrep, fallback to manual installation if not available
            if ! $PKG_INSTALL ripgrep 2>/dev/null; then
                print_warning "ripgrep not in apt repos, installing from GitHub releases..."
                local TEMP_DEB="$(mktemp)"
                local RG_VERSION="14.1.0"
                wget -O "$TEMP_DEB" "https://github.com/BurntSushi/ripgrep/releases/download/${RG_VERSION}/ripgrep_${RG_VERSION}-1_amd64.deb"
                sudo dpkg -i "$TEMP_DEB"
                rm -f "$TEMP_DEB"
            fi
            ;;
        dnf|yum)
            $PKG_INSTALL ripgrep
            ;;
        pacman)
            $PKG_INSTALL ripgrep
            ;;
        zypper)
            $PKG_INSTALL ripgrep
            ;;
        choco)
            $PKG_INSTALL ripgrep
            ;;
        scoop)
            $PKG_INSTALL ripgrep
            ;;
        *)
            print_error "Don't know how to install ripgrep with $PKG_MANAGER"
            return 1
            ;;
    esac
    
    if command_exists rg; then
        print_success "ripgrep installed successfully"
    else
        print_error "Failed to install ripgrep"
        return 1
    fi
}

# Install fzf
install_fzf() {
    if command_exists fzf; then
        print_success "fzf is already installed ($(fzf --version))"
        return 0
    fi
    
    print_step "Installing fzf..."
    
    case $PKG_MANAGER in
        brew)
            $PKG_INSTALL fzf
            ;;
        apt)
            $PKG_INSTALL fzf
            ;;
        dnf|yum)
            $PKG_INSTALL fzf
            ;;
        pacman)
            $PKG_INSTALL fzf
            ;;
        zypper)
            $PKG_INSTALL fzf
            ;;
        choco)
            $PKG_INSTALL fzf
            ;;
        scoop)
            $PKG_INSTALL fzf
            ;;
        *)
            # Fallback to git installation
            print_warning "Installing fzf from git..."
            git clone --depth 1 https://github.com/junegunn/fzf.git ~/.fzf
            ~/.fzf/install --all --no-bash --no-zsh --no-fish
            ;;
    esac
    
    if command_exists fzf; then
        print_success "fzf installed successfully"
    else
        print_error "Failed to install fzf"
        return 1
    fi
}

# Install git
install_git() {
    if command_exists git; then
        print_success "git is already installed ($(git --version))"
        return 0
    fi
    
    print_step "Installing git..."
    
    case $PKG_MANAGER in
        brew)
            $PKG_INSTALL git
            ;;
        apt)
            $PKG_INSTALL git
            ;;
        dnf|yum)
            $PKG_INSTALL git
            ;;
        pacman)
            $PKG_INSTALL git
            ;;
        zypper)
            $PKG_INSTALL git
            ;;
        choco)
            $PKG_INSTALL git
            ;;
        scoop)
            $PKG_INSTALL git
            ;;
        *)
            print_error "Don't know how to install git with $PKG_MANAGER"
            return 1
            ;;
    esac
    
    if command_exists git; then
        print_success "git installed successfully"
    else
        print_error "Failed to install git"
        return 1
    fi
}

# Install GitHub CLI
install_gh() {
    if command_exists gh; then
        print_success "GitHub CLI (gh) is already installed ($(gh --version | head -1))"
        return 0
    fi
    
    print_step "Installing GitHub CLI..."
    
    case $PKG_MANAGER in
        brew)
            $PKG_INSTALL gh
            ;;
        apt)
            # GitHub provides official apt repo
            print_step "Adding GitHub CLI apt repository..."
            curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg | sudo dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg
            echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" | sudo tee /etc/apt/sources.list.d/github-cli.list > /dev/null
            sudo apt update
            $PKG_INSTALL gh
            ;;
        dnf|yum)
            # GitHub provides official repo for RHEL-based distros
            sudo dnf config-manager --add-repo https://cli.github.com/packages/rpm/gh-cli.repo 2>/dev/null || \
            sudo yum-config-manager --add-repo https://cli.github.com/packages/rpm/gh-cli.repo
            $PKG_INSTALL gh
            ;;
        pacman)
            $PKG_INSTALL github-cli
            ;;
        zypper)
            $PKG_INSTALL gh
            ;;
        choco)
            $PKG_INSTALL gh
            ;;
        scoop)
            $PKG_INSTALL gh
            ;;
        *)
            print_warning "Don't know how to install GitHub CLI with $PKG_MANAGER"
            print_warning "Please install manually from: https://cli.github.com/"
            return 1
            ;;
    esac
    
    if command_exists gh; then
        print_success "GitHub CLI installed successfully"
    else
        print_error "Failed to install GitHub CLI"
        return 1
    fi
}

# Main function
main() {
    echo -e "${BLUE}╔════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║  Reliant Post-Installation Setup      ║${NC}"
    echo -e "${BLUE}╚════════════════════════════════════════╝${NC}"
    
    print_step "Detecting system configuration..."
    detect_system
    echo -e "Operating System: ${GREEN}$OS${NC}"
    echo -e "Package Manager: ${GREEN}$PKG_MANAGER${NC}"
    
    print_step "Checking and installing required tools..."
    
    local failed_installs=()
    
    # Install each tool
    install_git || failed_installs+=("git")
    install_ripgrep || failed_installs+=("ripgrep")
    install_fzf || failed_installs+=("fzf")
    install_gh || failed_installs+=("gh")
    
    echo ""
    if [ ${#failed_installs[@]} -eq 0 ]; then
        echo -e "${GREEN}╔════════════════════════════════════════╗${NC}"
        echo -e "${GREEN}║  All tools successfully installed! ✨  ║${NC}"
        echo -e "${GREEN}╚════════════════════════════════════════╝${NC}"
        echo ""
        print_success "git: $(git --version)"
        print_success "ripgrep: $(rg --version | head -1)"
        print_success "fzf: $(fzf --version)"
        print_success "gh: $(gh --version | head -1)"
    else
        echo -e "${YELLOW}╔════════════════════════════════════════╗${NC}"
        echo -e "${YELLOW}║  Setup completed with warnings        ║${NC}"
        echo -e "${YELLOW}╚════════════════════════════════════════╝${NC}"
        echo ""
        print_warning "Failed to install: ${failed_installs[*]}"
        echo "Please install these tools manually for full functionality."
    fi
}

# Run if executed directly
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
    main "$@"
fi