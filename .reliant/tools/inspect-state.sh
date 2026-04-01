#!/bin/bash
# Quick system state inspector for Reliant development
#
# Usage: ./.reliant/tools/inspect-state.sh
# Usage: ./.reliant/tools/inspect-state.sh --full

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

FULL_MODE=false
[ "$1" = "--full" ] && FULL_MODE=true

echo "======================================"
echo "Reliant Development State Inspector"
echo "======================================"
echo ""

# Load ports
if [ -f "$PROJECT_ROOT/.env.ports" ]; then
  source "$PROJECT_ROOT/.env.ports"
elif [ -f "$PROJECT_ROOT/.ports.json" ]; then
  BACKEND_PORT=$(grep -o '"backend":[^,}]*' "$PROJECT_ROOT/.ports.json" | cut -d: -f2 | tr -d ' ')
  FRONTEND_PORT=$(grep -o '"frontend":[^,}]*' "$PROJECT_ROOT/.ports.json" | cut -d: -f2 | tr -d ' ')
fi

# === Port Configuration ===
echo "📡 Ports"
echo "--------"
if [ -f "$PROJECT_ROOT/.env.ports" ]; then
  cat "$PROJECT_ROOT/.env.ports" | sed 's/^export /  /'
else
  echo "  (No .env.ports found - dev server may not be running)"
fi
echo ""

# === Health Check ===
echo "🏥 Health"
echo "---------"

if [ -n "$BACKEND_PORT" ]; then
  HEALTH=$(curl -s --connect-timeout 2 "http://localhost:${BACKEND_PORT}/api/v2/health" 2>/dev/null || echo "UNAVAILABLE")
  if [ "$HEALTH" = "UNAVAILABLE" ]; then
    echo "  Backend (port $BACKEND_PORT): ❌ Not responding"
  else
    echo "  Backend (port $BACKEND_PORT): ✅ Healthy"
  fi
else
  echo "  Backend: ❌ Port not configured"
fi

if [ -n "$FRONTEND_PORT" ]; then
  FRONTEND_STATUS=$(curl -s --connect-timeout 2 -o /dev/null -w "%{http_code}" "http://localhost:${FRONTEND_PORT}" 2>/dev/null || echo "000")
  if [ "$FRONTEND_STATUS" = "200" ] || [ "$FRONTEND_STATUS" = "304" ]; then
    echo "  Frontend (port $FRONTEND_PORT): ✅ Running"
  else
    echo "  Frontend (port $FRONTEND_PORT): ❌ Not responding (HTTP $FRONTEND_STATUS)"
  fi
fi

if [ -n "$TEMPORAL_UI_PORT" ]; then
  TEMPORAL_STATUS=$(curl -s --connect-timeout 2 -o /dev/null -w "%{http_code}" "http://localhost:${TEMPORAL_UI_PORT}" 2>/dev/null || echo "000")
  if [ "$TEMPORAL_STATUS" = "200" ] || [ "$TEMPORAL_STATUS" = "304" ]; then
    echo "  Temporal UI (port $TEMPORAL_UI_PORT): ✅ Running"
  else
    echo "  Temporal UI (port $TEMPORAL_UI_PORT): ❌ Not responding"
  fi
fi

if [ -n "$GRPC_PORT" ]; then
  # Check if gRPC port is listening
  if lsof -i tcp:$GRPC_PORT >/dev/null 2>&1; then
    echo "  gRPC (port $GRPC_PORT): ✅ Listening"
  else
    echo "  gRPC (port $GRPC_PORT): ❌ Not listening"
  fi
fi
echo ""

# === Database ===
echo "💾 Database"
echo "-----------"
DB_PATH="$PROJECT_ROOT/data/reliant.db"
TEMPORAL_DB="$PROJECT_ROOT/data/temporal.db"

if [ -f "$DB_PATH" ]; then
  DB_SIZE=$(ls -lh "$DB_PATH" | awk '{print $5}')
  echo "  Reliant DB: $DB_PATH ($DB_SIZE)"
  
  # Quick stats
  CHAT_COUNT=$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM chats" 2>/dev/null || echo "?")
  MSG_COUNT=$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM messages" 2>/dev/null || echo "?")
  ACTIVE_CHATS=$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM chats WHERE state = 'active'" 2>/dev/null || echo "?")
  RUNNING_WORKFLOWS=$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM workflows WHERE status = 'running'" 2>/dev/null || echo "?")
  
  echo "    Chats: $CHAT_COUNT (active: $ACTIVE_CHATS)"
  echo "    Messages: $MSG_COUNT"
  echo "    Running workflows: $RUNNING_WORKFLOWS"
else
  echo "  Reliant DB: ❌ Not found"
fi

if [ -f "$TEMPORAL_DB" ]; then
  TEMPORAL_SIZE=$(ls -lh "$TEMPORAL_DB" | awk '{print $5}')
  echo "  Temporal DB: $TEMPORAL_DB ($TEMPORAL_SIZE)"
else
  echo "  Temporal DB: ❌ Not found"
fi
echo ""

# === Logs ===
echo "📝 Logs"
echo "-------"
LOG_FILE="$PROJECT_ROOT/data/logs.txt"
BUILD_LOG="$PROJECT_ROOT/data/build-errors.log"

if [ -f "$LOG_FILE" ]; then
  LOG_SIZE=$(ls -lh "$LOG_FILE" | awk '{print $5}')
  LOG_LINES=$(wc -l < "$LOG_FILE" | tr -d ' ')
  echo "  Main log: $LOG_SIZE ($LOG_LINES lines)"
  
  # Recent errors
  RECENT_ERRORS=$(tail -100 "$LOG_FILE" | grep -ci "error" 2>/dev/null | tr -d ' ' || echo "0")
  if [ -n "$RECENT_ERRORS" ] && [ "$RECENT_ERRORS" -gt 0 ] 2>/dev/null; then
    echo "    ⚠️  $RECENT_ERRORS errors in last 100 lines"
  fi
else
  echo "  Main log: (not found)"
fi

if [ -f "$BUILD_LOG" ] && [ -s "$BUILD_LOG" ]; then
  echo "  Build errors: ⚠️  $(wc -l < "$BUILD_LOG" | tr -d ' ') lines"
else
  echo "  Build errors: ✅ None"
fi
echo ""

# === Docker (Temporal UI) ===
echo "🐳 Docker"
echo "---------"
if command -v docker >/dev/null 2>&1; then
  TEMPORAL_CONTAINER=$(docker ps --filter "name=temporal-ui-dev" --format "{{.Names}}: {{.Status}}" 2>/dev/null | head -1)
  if [ -n "$TEMPORAL_CONTAINER" ]; then
    echo "  $TEMPORAL_CONTAINER"
  else
    echo "  No Temporal UI container running"
  fi
else
  echo "  Docker not installed"
fi
echo ""

# === Full mode extras ===
if [ "$FULL_MODE" = true ]; then
  echo "🔍 Extended Information"
  echo "-----------------------"
  echo ""
  
  echo "Git Status:"
  cd "$PROJECT_ROOT"
  git status --short | head -10
  if [ $(git status --short | wc -l) -gt 10 ]; then
    echo "  ... and $(( $(git status --short | wc -l) - 10 )) more"
  fi
  echo ""
  
  echo "Recent Chats:"
  if [ -f "$DB_PATH" ]; then
    sqlite3 -header -column "$DB_PATH" "
      SELECT id, substr(title, 1, 30) as title, state, agent
      FROM chats ORDER BY last_active DESC LIMIT 5;
    " 2>/dev/null || echo "  (query failed)"
  fi
  echo ""
fi

echo "======================================"
echo "Run with --full for extended info"
echo "======================================"
