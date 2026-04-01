#!/bin/bash
# SQLite database query helper
#
# Usage: ./.reliant/tools/db-query.sh "SELECT * FROM chats LIMIT 5"
# Usage: ./.reliant/tools/db-query.sh tables
# Usage: ./.reliant/tools/db-query.sh schema TABLE_NAME
# Usage: ./.reliant/tools/db-query.sh recent-chats
# Usage: ./.reliant/tools/db-query.sh recent-messages CHAT_ID
# Usage: ./.reliant/tools/db-query.sh workflows
# Usage: ./.reliant/tools/db-query.sh stats

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

DB_PATH="${DB_PATH:-$PROJECT_ROOT/data/reliant.db}"

if [ ! -f "$DB_PATH" ]; then
  echo "Error: Database not found at $DB_PATH"
  echo "Make sure dev server has been started at least once."
  exit 1
fi

case "$1" in
  tables)
    echo "=== Tables in $DB_PATH ==="
    sqlite3 "$DB_PATH" ".tables"
    ;;
    
  schema)
    if [ -z "$2" ]; then
      echo "Usage: $0 schema TABLE_NAME"
      exit 1
    fi
    echo "=== Schema for $2 ==="
    sqlite3 "$DB_PATH" ".schema $2"
    ;;
    
  recent-chats)
    echo "=== Recent Chats ==="
    sqlite3 -header -column "$DB_PATH" "
      SELECT id, title, state, agent, workflow_id, 
             datetime(created_at) as created, 
             datetime(last_active) as last_active
      FROM chats 
      ORDER BY last_active DESC 
      LIMIT 10;
    "
    ;;
    
  recent-messages)
    if [ -z "$2" ]; then
      echo "=== Recent Messages (all chats) ==="
      sqlite3 -header -column "$DB_PATH" "
        SELECT m.id, m.chat_id, m.ordinal, m.role, m.agent, 
               datetime(m.created_at) as created
        FROM messages m
        ORDER BY m.created_at DESC 
        LIMIT 20;
      "
    else
      echo "=== Messages for Chat: $2 ==="
      sqlite3 -header -column "$DB_PATH" "
        SELECT id, ordinal, role, agent, input_tokens, output_tokens,
               datetime(created_at) as created
        FROM messages 
        WHERE chat_id = '$2'
        ORDER BY ordinal;
      "
    fi
    ;;
    
  workflows)
    echo "=== Active/Recent Workflows ==="
    sqlite3 -header -column "$DB_PATH" "
      SELECT id, workflow_name, status, chat_id,
             datetime(created_at) as created,
             datetime(completed_at) as completed
      FROM workflows
      ORDER BY created_at DESC
      LIMIT 15;
    "
    ;;
    
  stats)
    echo "=== Database Statistics ==="
    echo ""
    echo "Table counts:"
    for table in chats messages workflows projects settings; do
      count=$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM $table" 2>/dev/null || echo "N/A")
      printf "  %-20s %s\n" "$table:" "$count"
    done
    echo ""
    echo "Active chats:"
    sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM chats WHERE state = 'active'" 2>/dev/null || echo "0"
    echo ""
    echo "Running workflows:"
    sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM workflows WHERE status = 'running'" 2>/dev/null || echo "0"
    ;;
    
  help|--help|-h)
    echo "Database Query Helper"
    echo ""
    echo "Usage: $0 COMMAND [ARGS]"
    echo ""
    echo "Commands:"
    echo "  tables                  List all tables"
    echo "  schema TABLE            Show schema for a table"
    echo "  recent-chats            Show 10 most recent chats"
    echo "  recent-messages [CHAT]  Show recent messages (optionally for a specific chat)"
    echo "  workflows               Show recent workflows"
    echo "  stats                   Show database statistics"
    echo "  \"SQL QUERY\"             Run arbitrary SQL query"
    echo ""
    echo "Examples:"
    echo "  $0 tables"
    echo "  $0 schema messages"
    echo "  $0 recent-chats"
    echo "  $0 \"SELECT * FROM chats WHERE state = 'active'\""
    ;;
    
  *)
    if [ -z "$1" ]; then
      echo "Usage: $0 COMMAND [ARGS]"
      echo "Run '$0 help' for more information."
      exit 1
    fi
    # Run as SQL query
    sqlite3 -header -column "$DB_PATH" "$1"
    ;;
esac
