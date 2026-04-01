#!/bin/bash

# Prompt filter hook: filters and validates user prompts
# Can block prompts containing sensitive information

EVENT_JSON=$(cat)

# Extract the content from metadata
CONTENT=$(echo "$EVENT_JSON" | grep -o '"content":"[^"]*' | cut -d'"' -f4)

# Check for potential secrets or sensitive data patterns
SENSITIVE_PATTERNS=(
    # API Keys
    "sk-[a-zA-Z0-9]{48}"
    "pk_[a-zA-Z0-9]{32}"
    "api_key"
    "apikey"
    "access_token"
    "bearer [a-zA-Z0-9]+"
    
    # Passwords
    "password[:=]"
    "pwd[:=]"
    "passwd[:=]"
    
    # Credit card patterns (basic)
    "[0-9]{4}[- ]?[0-9]{4}[- ]?[0-9]{4}[- ]?[0-9]{4}"
    
    # SSN pattern
    "[0-9]{3}-[0-9]{2}-[0-9]{4}"
)

# Check for sensitive patterns
for pattern in "${SENSITIVE_PATTERNS[@]}"; do
    if echo "$CONTENT" | grep -qE "$pattern"; then
        cat <<EOF
{
  "block": true,
  "blockMessage": "Prompt contains potentially sensitive information. Please remove any secrets, API keys, or passwords before continuing."
}
EOF
        exit 0
    fi
done

# Check prompt length (prevent extremely long prompts)
CONTENT_LENGTH=${#CONTENT}
if [ $CONTENT_LENGTH -gt 10000 ]; then
    cat <<EOF
{
  "block": true,
  "blockMessage": "Prompt is too long (${CONTENT_LENGTH} characters). Please break it into smaller parts."
}
EOF
    exit 0
fi

# Allow the prompt
echo "{}"
exit 0