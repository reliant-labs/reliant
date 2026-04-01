#!/bin/bash

# Log tool use hook: logs all tool executions for audit
# Reads event JSON from stdin

EVENT_JSON=$(cat)

# Extract relevant fields
TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
SESSION_ID=$(echo "$EVENT_JSON" | grep -o '"sessionId":"[^"]*' | cut -d'"' -f4)
TOOL_NAME=$(echo "$EVENT_JSON" | grep -o '"name":"[^"]*' | cut -d'"' -f4)
TOOL_ERROR=$(echo "$EVENT_JSON" | grep -o '"error":"[^"]*' | cut -d'"' -f4)

# Log file location
LOG_FILE="/tmp/reliant-tool-usage.log"

# Create log entry
if [ -n "$TOOL_ERROR" ]; then
    echo "[$TIMESTAMP] Session: $SESSION_ID, Tool: $TOOL_NAME, Status: ERROR - $TOOL_ERROR" >> "$LOG_FILE"
else
    echo "[$TIMESTAMP] Session: $SESSION_ID, Tool: $TOOL_NAME, Status: SUCCESS" >> "$LOG_FILE"
fi

# Always allow (this is post-execution logging)
echo "{}"
exit 0