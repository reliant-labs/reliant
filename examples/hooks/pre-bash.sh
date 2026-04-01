#!/bin/bash

# Pre-bash hook: validates and potentially blocks dangerous bash commands
# Reads event JSON from stdin, returns response JSON to stdout

# Read the event JSON
EVENT_JSON=$(cat)

# Extract tool input using jq (if available) or basic parsing
TOOL_NAME=$(echo "$EVENT_JSON" | grep -o '"name":"[^"]*' | cut -d'"' -f4)
TOOL_INPUT=$(echo "$EVENT_JSON" | grep -o '"input":{[^}]*' | sed 's/"input"://')

# Extract the command from the input (assuming it has a "command" field)
COMMAND=$(echo "$TOOL_INPUT" | grep -o '"command":"[^"]*' | cut -d'"' -f4)

# List of dangerous patterns to block
DANGEROUS_PATTERNS=(
    "rm -rf /"
    "rm -rf /*"
    ":(){ :|:& };:"  # Fork bomb
    "> /dev/sda"
    "mkfs."
    "dd if=/dev/zero"
    "chmod -R 777 /"
)

# Check for dangerous patterns
for pattern in "${DANGEROUS_PATTERNS[@]}"; do
    if [[ "$COMMAND" == *"$pattern"* ]]; then
        # Block the command
        cat <<EOF
{
  "block": true,
  "blockMessage": "Command blocked for safety: contains dangerous pattern '$pattern'"
}
EOF
        exit 0
    fi
done

# Check for sudo commands (warn but don't block)
if [[ "$COMMAND" == *"sudo"* ]]; then
    echo "Warning: Command contains 'sudo': $COMMAND" >&2
fi

# Allow the command
echo "{}"
exit 0