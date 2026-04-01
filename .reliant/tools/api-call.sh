#!/bin/bash
# Usage: ./.reliant/tools/api-call.sh GET /health
# Usage: ./.reliant/tools/api-call.sh POST /api/v1/sessions '{"name": "test"}'
# Usage: ./.reliant/tools/api-call.sh GET /api/v1/chats -v (verbose mode)

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

METHOD="${1:-GET}"
ENDPOINT="$2"
BODY="$3"

# Check for verbose flag
VERBOSE=""
for arg in "$@"; do
  if [ "$arg" = "-v" ] || [ "$arg" = "--verbose" ]; then
    VERBOSE="-v"
  fi
done

if [ -z "$ENDPOINT" ]; then
  echo "Usage: $0 METHOD ENDPOINT [BODY] [-v]"
  echo ""
  echo "Examples:"
  echo "  $0 GET /api/v2/health"
  echo "  $0 GET /api/v1/chats"
  echo "  $0 POST /api/v1/chats '{\"title\": \"New Chat\"}'"
  echo "  $0 GET /api/v1/projects -v  # verbose mode"
  exit 1
fi

# Load port configuration
if [ -f "$PROJECT_ROOT/.env.ports" ]; then
  source "$PROJECT_ROOT/.env.ports"
elif [ -f "$PROJECT_ROOT/.ports.json" ]; then
  BACKEND_PORT=$(grep -o '"backend":[^,}]*' "$PROJECT_ROOT/.ports.json" | cut -d: -f2 | tr -d ' ')
fi

if [ -z "$BACKEND_PORT" ]; then
  echo "Error: Could not determine backend port"
  echo "Make sure dev server is running (./scripts/dev.sh)"
  exit 1
fi

BASE_URL="http://localhost:${BACKEND_PORT}"

echo "→ $METHOD $BASE_URL$ENDPOINT"

if [ -n "$BODY" ] && [ "$BODY" != "-v" ] && [ "$BODY" != "--verbose" ]; then
  curl -s $VERBOSE -X "$METHOD" \
    -H "Content-Type: application/json" \
    -d "$BODY" \
    "${BASE_URL}${ENDPOINT}" | jq . 2>/dev/null || cat
else
  curl -s $VERBOSE -X "$METHOD" \
    -H "Content-Type: application/json" \
    "${BASE_URL}${ENDPOINT}" | jq . 2>/dev/null || cat
fi

echo ""
