#!/bin/bash

# On complete hook: runs when agent completes processing
# Can be used for notifications, cleanup, or post-processing

EVENT_JSON=$(cat)

# Extract session information
SESSION_ID=$(echo "$EVENT_JSON" | grep -o '"sessionId":"[^"]*' | cut -d'"' -f4)
TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

# Log completion
echo "[$TIMESTAMP] Session $SESSION_ID completed" >> /tmp/reliant-completions.log

# Send notification (example - requires terminal-notifier on macOS)
if command -v terminal-notifier &> /dev/null; then
    terminal-notifier -title "Reliant" -message "Session completed: $SESSION_ID" -sound default 2>/dev/null
elif command -v notify-send &> /dev/null; then
    notify-send "Reliant" "Session completed: $SESSION_ID" 2>/dev/null
fi

# Perform any cleanup if needed
# For example, clear temporary files created during the session
if [ -d "/tmp/reliant-session-$SESSION_ID" ]; then
    rm -rf "/tmp/reliant-session-$SESSION_ID"
fi

# Always allow (this is post-processing)
echo "{}"
exit 0