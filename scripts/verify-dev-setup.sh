#!/bin/bash

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}Verifying Development Setup${NC}\n"

# Function to check if command exists
command_exists() {
    command -v "$1" >/dev/null 2>&1
}

# Check Go
if command_exists go; then
    GO_VERSION=$(go version | awk '{print $3}')
    echo -e "${GREEN}✅ Go${NC} - $GO_VERSION"
else
    echo -e "${RED}❌ Go is not installed${NC}"
    exit 1
fi

# Check Node
if command_exists node; then
    NODE_VERSION=$(node --version)
    echo -e "${GREEN}✅ Node.js${NC} - $NODE_VERSION"
else
    echo -e "${RED}❌ Node.js is not installed${NC}"
    exit 1
fi

# Check npm
if command_exists npm; then
    NPM_VERSION=$(npm --version)
    echo -e "${GREEN}✅ npm${NC} - v$NPM_VERSION"
else
    echo -e "${RED}❌ npm is not installed${NC}"
    exit 1
fi

# Check Air
if command_exists air; then
    AIR_VERSION=$(air -v 2>&1 | head -1)
    echo -e "${GREEN}✅ Air${NC} - $AIR_VERSION"
else
    echo -e "${RED}❌ Air is not installed${NC}"
    echo -e "${YELLOW}   Install with: go install github.com/air-verse/air@latest${NC}"
    exit 1
fi

# Check Docker
if command_exists docker; then
    if docker ps >/dev/null 2>&1; then
        DOCKER_VERSION=$(docker --version | awk '{print $3}' | sed 's/,//')
        echo -e "${GREEN}✅ Docker${NC} - v$DOCKER_VERSION (running)"
    else
        echo -e "${YELLOW}⚠️  Docker${NC} - installed but daemon not running"
        echo -e "${YELLOW}   Start Docker Desktop before running 'npm run dev'${NC}"
    fi
else
    echo -e "${RED}❌ Docker is not installed${NC}"
    echo -e "${YELLOW}   Install Docker Desktop: https://www.docker.com/products/docker-desktop${NC}"
    exit 1
fi

# Check directories
echo ""
if [ -d ".dev" ]; then
    echo -e "${GREEN}✅ .dev directory${NC} exists"
else
    echo -e "${YELLOW}⚠️  .dev directory${NC} will be created on first run"
fi

if [ -d "dist" ]; then
    echo -e "${GREEN}✅ dist directory${NC} exists"
else
    echo -e "${YELLOW}⚠️  dist directory${NC} will be created on first run"
fi

# Check for V2 backend source
if [ -f "cmd/reliant/main.go" ]; then
    echo -e "${GREEN}✅ V2 backend source${NC} found"
else
    echo -e "${RED}❌ V2 backend source${NC} not found at cmd/reliant/main.go"
    exit 1
fi

# Check Air config
if [ -f ".air.toml" ]; then
    echo -e "${GREEN}✅ Air config${NC} found (.air.toml)"
else
    echo -e "${RED}❌ Air config${NC} not found (.air.toml)"
    exit 1
fi

# Check dev script
if [ -x "scripts/dev.sh" ]; then
    echo -e "${GREEN}✅ Dev script${NC} found and executable (scripts/dev.sh)"
else
    echo -e "${RED}❌ Dev script${NC} not found or not executable"
    exit 1
fi

# Check Proxyman script
PROXYMAN_SCRIPT="$HOME/Library/Application Support/com.proxyman.NSProxy/app-data/proxyman_env_automatic_setup.sh"
echo ""
if [ -f "$PROXYMAN_SCRIPT" ]; then
    echo -e "${GREEN}✅ Proxyman${NC} - setup script found"
else
    echo -e "${YELLOW}⚠️  Proxyman${NC} - setup script not found (will be skipped)"
    echo -e "${YELLOW}   This is OK if you don't use Proxyman${NC}"
fi

# Check npm dependencies
echo ""
if [ -d "web/node_modules" ]; then
    echo -e "${GREEN}✅ Web dependencies${NC} installed"
else
    echo -e "${YELLOW}⚠️  Web dependencies${NC} not installed"
    echo -e "${YELLOW}   Run 'npm install' in the web directory${NC}"
fi

if [ -d "electron/node_modules" ]; then
    echo -e "${GREEN}✅ Electron dependencies${NC} installed"
else
    echo -e "${YELLOW}⚠️  Electron dependencies${NC} not installed"
    echo -e "${YELLOW}   Run 'npm install' in the electron directory${NC}"
fi

echo ""
echo -e "${GREEN}✅ Development setup verification complete!${NC}"
echo ""
echo -e "${BLUE}Ready to start development:${NC}"
echo -e "  ${YELLOW}npm run dev${NC}"
echo ""
echo -e "${BLUE}To enable Proxyman (disabled by default):${NC}"
echo -e "  ${YELLOW}RELIANT_PROXYMAN=1 npm run dev${NC}"