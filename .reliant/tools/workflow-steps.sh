#!/bin/bash
# workflow-steps.sh - Extract and pretty print workflow steps for a chat
#
# Usage: ./workflow-steps.sh <chat_id> [--raw]
#
# Options:
#   --raw    Output raw JSON instead of pretty printed table

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
DB_PATH="$PROJECT_ROOT/data/reliant.db"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
GRAY='\033[0;90m'
BOLD='\033[1m'
NC='\033[0m' # No Color

usage() {
    echo "Usage: $0 <chat_id> [options]"
    echo ""
    echo "Extract and display workflow step executions for a chat."
    echo ""
    echo "Options:"
    echo "  --raw     Show raw query output"
    echo "  --json    Output as JSON"
    echo "  --all     Include internal steps (e.g., *-save steps)"
    echo "  --help    Show this help"
    echo ""
    echo "Examples:"
    echo "  $0 abc123-def456"
    echo "  $0 abc123-def456 --json"
    echo "  $0 abc123-def456 --all   # include internal -save steps"
    exit 1
}

if [ -z "$1" ] || [ "$1" == "--help" ]; then
    usage
fi

CHAT_ID="$1"
RAW_MODE=false
JSON_MODE=false
SHOW_ALL=false

shift
while [ $# -gt 0 ]; do
    case "$1" in
        --raw)
            RAW_MODE=true
            ;;
        --json)
            JSON_MODE=true
            ;;
        --all)
            SHOW_ALL=true
            ;;
        *)
            echo "Unknown option: $1"
            usage
            ;;
    esac
    shift
done

# Build filter clause for internal steps
if [ "$SHOW_ALL" = true ]; then
    STEP_FILTER=""
else
    STEP_FILTER="AND se.step_id NOT LIKE '%-save'"
fi

if [ ! -f "$DB_PATH" ]; then
    echo -e "${RED}Error: Database not found at $DB_PATH${NC}"
    exit 1
fi

# First, get the workflow IDs associated with this chat
WORKFLOW_IDS=$(sqlite3 "$DB_PATH" "SELECT id FROM workflows WHERE chat_id = '$CHAT_ID' ORDER BY created_at;")

if [ -z "$WORKFLOW_IDS" ]; then
    echo -e "${YELLOW}No workflows found for chat: $CHAT_ID${NC}"
    exit 0
fi

if [ "$JSON_MODE" = true ]; then
    # JSON output mode
    sqlite3 -json "$DB_PATH" "
        SELECT 
            se.id,
            se.workflow_id,
            se.step_id,
            se.activity_name,
            se.exit_code,
            se.success,
            se.duration_ms,
            se.loop_node_id,
            se.loop_iteration,
            se.created_at,
            w.workflow_name,
            w.thread
        FROM step_executions se
        JOIN workflows w ON se.workflow_id = w.id
        WHERE w.chat_id = '$CHAT_ID'
        $STEP_FILTER
        ORDER BY se.created_at ASC;
    "
    exit 0
fi

if [ "$RAW_MODE" = true ]; then
    # Raw output mode
    sqlite3 -header -column "$DB_PATH" "
        SELECT 
            se.step_id,
            se.activity_name,
            se.loop_node_id,
            se.loop_iteration,
            se.exit_code,
            se.success,
            se.duration_ms,
            se.created_at
        FROM step_executions se
        JOIN workflows w ON se.workflow_id = w.id
        WHERE w.chat_id = '$CHAT_ID'
        $STEP_FILTER
        ORDER BY se.created_at ASC;
    "
    exit 0
fi

# Pretty print mode
echo ""
echo -e "${BOLD}Workflow Steps for Chat: ${CYAN}$CHAT_ID${NC}"
echo -e "${GRAY}════════════════════════════════════════════════════════════════════${NC}"

# Get workflow info
echo ""
echo -e "${BOLD}Workflows:${NC}"
sqlite3 "$DB_PATH" "
    SELECT id, workflow_name, status, thread, created_at
    FROM workflows 
    WHERE chat_id = '$CHAT_ID'
    ORDER BY created_at;
" | while IFS='|' read -r wf_id wf_name status thread created; do
    case "$status" in
        completed) status_color="${GREEN}" ;;
        running)   status_color="${YELLOW}" ;;
        failed)    status_color="${RED}" ;;
        cancelled) status_color="${GRAY}" ;;
        *)         status_color="${NC}" ;;
    esac
    echo -e "  ${BLUE}$wf_id${NC}"
    echo -e "    Name:   ${BOLD}$wf_name${NC}"
    echo -e "    Status: ${status_color}$status${NC}"
    echo -e "    Thread: $thread"
    echo -e "    Created: $created"
    echo ""
done

# Get step executions grouped by loop
echo -e "${BOLD}Step Executions:${NC}"
echo ""

# Query to get steps with loop grouping info
STEPS=$(sqlite3 "$DB_PATH" "
    SELECT 
        se.step_id,
        se.activity_name,
        COALESCE(se.loop_node_id, '') as loop_node_id,
        COALESCE(se.loop_iteration, -1) as loop_iteration,
        COALESCE(se.exit_code, '') as exit_code,
        COALESCE(se.success, '') as success,
        COALESCE(se.duration_ms, '') as duration_ms,
        datetime(se.created_at) as created_at,
        w.workflow_name
    FROM step_executions se
    JOIN workflows w ON se.workflow_id = w.id
    WHERE w.chat_id = '$CHAT_ID'
    $STEP_FILTER
    ORDER BY se.created_at ASC;
")

current_loop=""
current_iteration=-1

echo "$STEPS" | while IFS='|' read -r step_id activity loop_id loop_iter exit_code success duration created wf_name; do
    # Skip empty lines
    [ -z "$step_id" ] && continue
    
    # Format activity name
    activity_short="${activity#V2_}"
    
    # Determine status icon and color
    if [ "$success" = "1" ]; then
        status_icon="✓"
        status_color="${GREEN}"
    elif [ "$success" = "0" ]; then
        status_icon="✗"
        status_color="${RED}"
    else
        status_icon="?"
        status_color="${GRAY}"
    fi
    
    # Format duration
    if [ -n "$duration" ] && [ "$duration" != "" ]; then
        if [ "$duration" -lt 1000 ]; then
            duration_fmt="${duration}ms"
        elif [ "$duration" -lt 60000 ]; then
            duration_fmt="$(echo "scale=1; $duration/1000" | bc)s"
        else
            mins=$((duration / 60000))
            secs=$(((duration % 60000) / 1000))
            duration_fmt="${mins}m ${secs}s"
        fi
    else
        duration_fmt="-"
    fi
    
    # Handle loop grouping
    indent=""
    loop_prefix=""
    
    if [ -n "$loop_id" ] && [ "$loop_id" != "" ]; then
        # Check if this is a new loop or new iteration
        if [ "$loop_id" != "$current_loop" ]; then
            current_loop="$loop_id"
            current_iteration=-1
            echo -e "  ${BOLD}${CYAN}⟳ Loop: $loop_id${NC}"
        fi
        
        if [ "$loop_iter" != "$current_iteration" ] && [ "$loop_iter" != "-1" ]; then
            current_iteration="$loop_iter"
            echo -e "    ${GRAY}── Iteration $loop_iter ──${NC}"
        fi
        
        indent="      "
    else
        # Reset loop tracking for non-loop steps
        if [ -n "$current_loop" ]; then
            current_loop=""
            current_iteration=-1
            echo ""
        fi
        indent="  "
    fi
    
    # Print step
    echo -e "${indent}${status_color}${status_icon}${NC} ${BOLD}$step_id${NC} ${GRAY}($activity_short)${NC}"
    
    # Print details on second line
    details=""
    if [ -n "$exit_code" ] && [ "$exit_code" != "" ]; then
        if [ "$exit_code" = "0" ]; then
            details="${details}exit=${GREEN}$exit_code${NC} "
        else
            details="${details}exit=${RED}$exit_code${NC} "
        fi
    fi
    details="${details}${GRAY}duration=$duration_fmt${NC}"
    
    echo -e "${indent}  ${details}"
done

echo ""
echo -e "${GRAY}────────────────────────────────────────────────────────────────────${NC}"

# Summary stats
echo ""
echo -e "${BOLD}Summary:${NC}"

# Read stats into variables properly
read -r total succeeded failed in_loop num_loops total_duration <<< $(sqlite3 "$DB_PATH" "
    SELECT 
        COUNT(*),
        COALESCE(SUM(CASE WHEN success = 1 THEN 1 ELSE 0 END), 0),
        COALESCE(SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END), 0),
        COALESCE(SUM(CASE WHEN loop_node_id IS NOT NULL AND loop_node_id != '' THEN 1 ELSE 0 END), 0),
        COUNT(DISTINCT CASE WHEN loop_node_id IS NOT NULL AND loop_node_id != '' THEN loop_node_id END),
        COALESCE(SUM(duration_ms), 0)
    FROM step_executions se
    JOIN workflows w ON se.workflow_id = w.id
    WHERE w.chat_id = '$CHAT_ID'
    $STEP_FILTER;
" | tr '|' ' ')

# Format total duration
if [ -n "$total_duration" ] && [ "$total_duration" != "" ] && [ "$total_duration" -gt 0 ]; then
    if [ "$total_duration" -lt 1000 ]; then
        total_duration_fmt="${total_duration}ms"
    elif [ "$total_duration" -lt 60000 ]; then
        total_duration_fmt="$(echo "scale=1; $total_duration/1000" | bc)s"
    else
        mins=$((total_duration / 60000))
        secs=$(((total_duration % 60000) / 1000))
        total_duration_fmt="${mins}m ${secs}s"
    fi
else
    total_duration_fmt="0ms"
fi

echo -e "  Total steps:    $total"
echo -e "  Succeeded:      ${GREEN}$succeeded${NC}"
echo -e "  Failed:         ${RED}$failed${NC}"
echo -e "  In loops:       $in_loop"
echo -e "  Unique loops:   $num_loops"
echo -e "  Total duration: $total_duration_fmt"
echo ""
