#!/bin/bash
set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
MAGENTA='\033[0;35m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

echo -e "${BLUE}🚀 Starting Reliant Multi-Instance Development${NC}"
echo ""

# Get current git worktree info
WORKTREE_PATH=$(pwd)
BRANCH_NAME=$(git branch --show-current 2>/dev/null || echo "unknown")
REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null || pwd)

# Check if we're in a worktree
IS_WORKTREE=false
if [ -f "$(git rev-parse --git-dir 2>/dev/null)/gitdir" ]; then
  IS_WORKTREE=true
  MAIN_REPO_PATH=$(git rev-parse --git-common-dir 2>/dev/null | sed 's/.git$//')
else
  MAIN_REPO_PATH=$REPO_ROOT
fi

echo -e "${GREEN}Instance Information:${NC}"
echo -e "  📁 Working Directory: ${BLUE}$WORKTREE_PATH${NC}"
echo -e "  🌿 Branch: ${BLUE}$BRANCH_NAME${NC}"
if [ "$IS_WORKTREE" = true ]; then
  echo -e "  🌳 Git Worktree: ${CYAN}Yes${NC}"
  echo -e "  📂 Main Repository: ${BLUE}$MAIN_REPO_PATH${NC}"
else
  echo -e "  🌳 Git Worktree: ${YELLOW}No (main repository)${NC}"
fi
echo ""

echo -e "${YELLOW}💡 Tips:${NC}"
echo -e "  • Run ${MAGENTA}npm run dev:multi${NC} again for another instance"
echo -e "  • Use ${CYAN}npm run instances${NC} to see all running instances"
echo -e "  • Press ${RED}Ctrl+C${NC} in this terminal to stop this instance"
echo ""

echo -e "${GREEN}🎯 Starting development environment...${NC}"
echo -e "${BLUE}ℹ️  Port allocation uses global locking to prevent conflicts${NC}"
echo ""

# Export MULTI_INSTANCE flag to skip port killing and force fresh port discovery
export MULTI_INSTANCE=true

# Start the development environment
# dev.sh now handles all port finding with proper locking
./scripts/dev.sh
