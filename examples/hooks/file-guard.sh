#!/bin/bash

# File guard hook: protects important files from modification
# Reads event JSON from stdin

EVENT_JSON=$(cat)

# Extract file path from the tool input
FILE_PATH=$(echo "$EVENT_JSON" | grep -o '"path":"[^"]*' | cut -d'"' -f4)

# List of protected file patterns
PROTECTED_PATTERNS=(
    "/etc/passwd"
    "/etc/shadow"
    "/etc/sudoers"
    ".ssh/id_rsa"
    ".ssh/authorized_keys"
    ".git/config"
    ".env"
    "*.key"
    "*.pem"
)

# Check if file matches protected patterns
for pattern in "${PROTECTED_PATTERNS[@]}"; do
    if [[ "$FILE_PATH" == $pattern ]] || [[ "$FILE_PATH" == *"$pattern" ]]; then
        # Block the operation
        cat <<EOF
{
  "block": true,
  "blockMessage": "Operation blocked: '$FILE_PATH' is a protected file"
}
EOF
        exit 0
    fi
done

# Allow the operation
echo "{}"
exit 0