#!/bin/bash
# Temporal workflow query helper
#
# Usage: ./.reliant/tools/temporal-query.sh workflows
# Usage: ./.reliant/tools/temporal-query.sh workflow WORKFLOW_ID
# Usage: ./.reliant/tools/temporal-query.sh history WORKFLOW_ID RUN_ID

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Load ports
if [ -f "$PROJECT_ROOT/.env.ports" ]; then
  source "$PROJECT_ROOT/.env.ports"
fi

TEMPORAL_ADDRESS="${TEMPORAL_ADDRESS:-localhost:${TEMPORAL_FRONTEND_PORT:-7233}}"

# Check if tctl is available
if ! command -v tctl >/dev/null 2>&1; then
  echo "Note: tctl not installed. Using database queries instead."
  echo "Install tctl: go install go.temporal.io/server/cmd/tctl@latest"
  echo ""
  USE_DB=true
else
  USE_DB=false
fi

DB_PATH="$PROJECT_ROOT/data/reliant.db"

case "$1" in
  workflows|list)
    echo "=== Active Workflows ==="
    if [ "$USE_DB" = true ]; then
      sqlite3 -header -column "$DB_PATH" "
        SELECT 
          w.id,
          w.workflow_name,
          w.status,
          c.title as chat_title,
          datetime(w.created_at) as started
        FROM workflows w
        LEFT JOIN chats c ON w.chat_id = c.id
        WHERE w.status = 'running'
        ORDER BY w.created_at DESC
        LIMIT 20;
      "
    else
      tctl --address "$TEMPORAL_ADDRESS" workflow list --open
    fi
    ;;
    
  all)
    echo "=== All Recent Workflows ==="
    if [ "$USE_DB" = true ]; then
      sqlite3 -header -column "$DB_PATH" "
        SELECT 
          w.id,
          w.workflow_name,
          w.status,
          datetime(w.created_at) as started,
          datetime(w.completed_at) as completed
        FROM workflows w
        ORDER BY w.created_at DESC
        LIMIT 30;
      "
    else
      tctl --address "$TEMPORAL_ADDRESS" workflow list
    fi
    ;;
    
  workflow|show)
    if [ -z "$2" ]; then
      echo "Usage: $0 workflow WORKFLOW_ID"
      exit 1
    fi
    WORKFLOW_ID="$2"
    echo "=== Workflow: $WORKFLOW_ID ==="
    
    if [ "$USE_DB" = true ]; then
      echo ""
      echo "Database Info:"
      sqlite3 -header -column "$DB_PATH" "
        SELECT * FROM workflows WHERE id = '$WORKFLOW_ID';
      "
      
      echo ""
      echo "Messages in workflow:"
      sqlite3 -header -column "$DB_PATH" "
        SELECT id, ordinal, role, agent, datetime(created_at) as created
        FROM messages 
        WHERE workflow_id = '$WORKFLOW_ID'
        ORDER BY ordinal;
      "
    else
      tctl --address "$TEMPORAL_ADDRESS" workflow describe --workflow_id "$WORKFLOW_ID"
    fi
    ;;
    
  history)
    if [ -z "$2" ] || [ -z "$3" ]; then
      echo "Usage: $0 history WORKFLOW_ID RUN_ID"
      exit 1
    fi
    WORKFLOW_ID="$2"
    RUN_ID="$3"
    
    if [ "$USE_DB" = true ]; then
      echo "History queries require tctl. Database shows limited info:"
      sqlite3 -header -column "$DB_PATH" "
        SELECT * FROM workflows WHERE id = '$WORKFLOW_ID';
      "
    else
      tctl --address "$TEMPORAL_ADDRESS" workflow showhistory \
        --workflow_id "$WORKFLOW_ID" \
        --run_id "$RUN_ID"
    fi
    ;;
    
  cancel)
    if [ -z "$2" ]; then
      echo "Usage: $0 cancel WORKFLOW_ID"
      exit 1
    fi
    WORKFLOW_ID="$2"
    
    if [ "$USE_DB" = true ]; then
      echo "Cancellation requires tctl or the API."
      echo "Try: curl -X POST http://localhost:$BACKEND_PORT/api/v1/workflows/$WORKFLOW_ID/cancel"
    else
      tctl --address "$TEMPORAL_ADDRESS" workflow cancel --workflow_id "$WORKFLOW_ID"
    fi
    ;;
    
  ui)
    if [ -n "$TEMPORAL_UI_PORT" ]; then
      echo "Opening Temporal UI at http://localhost:$TEMPORAL_UI_PORT"
      open "http://localhost:$TEMPORAL_UI_PORT" 2>/dev/null || \
        xdg-open "http://localhost:$TEMPORAL_UI_PORT" 2>/dev/null || \
        echo "Visit: http://localhost:$TEMPORAL_UI_PORT"
    else
      echo "Temporal UI port not configured"
    fi
    ;;
    
  help|--help|-h)
    echo "Temporal Workflow Query Helper"
    echo ""
    echo "Usage: $0 COMMAND [ARGS]"
    echo ""
    echo "Commands:"
    echo "  workflows, list       List active/running workflows"
    echo "  all                   List all recent workflows"
    echo "  workflow ID           Show details for a workflow"
    echo "  history ID RUN_ID     Show workflow execution history"
    echo "  cancel ID             Cancel a running workflow"
    echo "  ui                    Open Temporal UI in browser"
    echo ""
    echo "Environment:"
    echo "  TEMPORAL_ADDRESS: $TEMPORAL_ADDRESS"
    echo "  TEMPORAL_UI_PORT: ${TEMPORAL_UI_PORT:-not set}"
    ;;
    
  *)
    echo "Usage: $0 COMMAND [ARGS]"
    echo "Run '$0 help' for more information."
    exit 1
    ;;
esac
