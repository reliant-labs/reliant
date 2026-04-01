#!/bin/bash

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
MAGENTA='\033[0;35m'
NC='\033[0m' # No Color

echo -e "${BLUE}📋 Reliant Multi-Instance Status${NC}"
echo -e "${CYAN}══════════════════════════════════════════════════════════════${NC}"
echo ""

# Function to check if a port is in use
check_port() {
  lsof -i tcp:$1 >/dev/null 2>&1
  return $?
}

INSTANCE_COUNT=0
RUNNING_COUNT=0

# Find all instance info files
for INSTANCE_FILE in .instance-*.info; do
  if [ -f "$INSTANCE_FILE" ]; then
    INSTANCE_COUNT=$((INSTANCE_COUNT + 1))

    # Read instance info
    source "$INSTANCE_FILE"

    # Check if ports are active
    FE_STATUS="${RED}✗ Stopped${NC}"
    BE_STATUS="${RED}✗ Stopped${NC}"
    INSTANCE_STATUS="${RED}Stopped${NC}"

    if check_port "$FRONTEND_PORT"; then
      FE_STATUS="${GREEN}✓ Running${NC}"
    fi

    if check_port "$BACKEND_PORT"; then
      BE_STATUS="${GREEN}✓ Running${NC}"
    fi

    if check_port "$FRONTEND_PORT" && check_port "$BACKEND_PORT"; then
      INSTANCE_STATUS="${GREEN}Running${NC}"
      RUNNING_COUNT=$((RUNNING_COUNT + 1))
    elif check_port "$FRONTEND_PORT" || check_port "$BACKEND_PORT"; then
      INSTANCE_STATUS="${YELLOW}Partial${NC}"
    fi

    # Check if process is still running
    PROCESS_STATUS=""
    if [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; then
      PROCESS_STATUS="${GREEN}(PID: $PID)${NC}"
    else
      PROCESS_STATUS="${RED}(Process ended)${NC}"
      # Clean up stale instance file
      rm -f "$INSTANCE_FILE"
      continue
    fi

    # Display instance info
    echo -e "${MAGENTA}Instance #$INSTANCE_COUNT${NC} - $INSTANCE_STATUS $PROCESS_STATUS"
    echo -e "  📁 Directory: ${BLUE}$WORKTREE_PATH${NC}"
    echo -e "  🌿 Branch: ${CYAN}$BRANCH_NAME${NC}"
    echo -e "  🌐 Frontend: Port ${YELLOW}$FRONTEND_PORT${NC} - $FE_STATUS"
    echo -e "  ⚙️  Backend:  Port ${YELLOW}$BACKEND_PORT${NC} - $BE_STATUS"
    if [ -n "$START_TIME" ]; then
      echo -e "  🕐 Started: ${NC}$START_TIME${NC}"
    fi
    echo -e "  🔗 URLs:"
    echo -e "     Frontend: ${BLUE}http://localhost:$FRONTEND_PORT${NC}"
    echo -e "     Backend:  ${BLUE}http://localhost:$BACKEND_PORT${NC}"
    echo ""
  fi
done

if [ $INSTANCE_COUNT -eq 0 ]; then
  echo -e "${YELLOW}No Reliant instances running.${NC}"
  echo -e "Run ${CYAN}npm run dev:multi${NC} to start a new instance."
else
  echo -e "${CYAN}══════════════════════════════════════════════════════════════${NC}"
  echo -e "${GREEN}Summary:${NC} $RUNNING_COUNT of $INSTANCE_COUNT instances running"
fi

echo ""
echo -e "${YELLOW}Commands:${NC}"
echo -e "  • ${CYAN}npm run dev:multi${NC} - Start a new instance with unique ports"
echo -e "  • ${CYAN}npm run instances${NC} - Show this status"
echo -e "  • ${RED}Ctrl+C${NC} in instance terminal - Stop that instance"
echo ""