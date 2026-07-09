#!/bin/bash
# Post-rebase hook - automatically fixes migration conflicts after rebase
# This should be installed as .git/hooks/post-rebase

MIGRATION_DIR="internal/db/migrations/postgres"

# Only run if migrations directory exists
if [ ! -d "$MIGRATION_DIR" ]; then
    exit 0
fi

# Check if any migration files were affected by the rebase
MIGRATION_CHANGES=$(git diff --name-only HEAD@{1} HEAD -- "$MIGRATION_DIR/*.sql" 2>/dev/null || true)

if [ -z "$MIGRATION_CHANGES" ]; then
    # No migration changes, skip
    exit 0
fi

echo ""
echo "🔍 Detected migration changes after rebase, checking for conflicts..."

# Run the fix script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -x "$SCRIPT_DIR/fix-migration-conflicts.sh" ]; then
    "$SCRIPT_DIR/fix-migration-conflicts.sh"
else
    echo "⚠️  Warning: fix-migration-conflicts.sh not found or not executable"
fi

exit 0
