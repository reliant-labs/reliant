#!/bin/bash
# Wrapper script for rebasing that automatically fixes migration conflicts
# Usage: ./scripts/rebase-with-migration-fix.sh [branch]

set -e

# Get target branch (default to main)
TARGET_BRANCH="${1:-main}"
MAIN_BRANCH=$(git rev-parse --abbrev-ref origin/HEAD 2>/dev/null | sed 's/origin\///' || echo "main")

if [ "$TARGET_BRANCH" = "main" ]; then
    TARGET_BRANCH="$MAIN_BRANCH"
fi

echo "🔄 Rebasing onto origin/$TARGET_BRANCH..."
echo ""

# Fetch latest
echo "📡 Fetching latest changes..."
git fetch origin "$TARGET_BRANCH"

# Show migration status before rebase
MIGRATION_DIR="internal/db/migrations/sqlite"
if [ -d "$MIGRATION_DIR" ]; then
    echo ""
    echo "📋 Current migrations in your branch:"
    BRANCH_MIGRATIONS=$(git diff --name-only --diff-filter=A "origin/$TARGET_BRANCH" HEAD -- "$MIGRATION_DIR/*.sql" 2>/dev/null || true)
    if [ -n "$BRANCH_MIGRATIONS" ]; then
        echo "$BRANCH_MIGRATIONS" | while read file; do
            [ -n "$file" ] && echo "   - $(basename $file)"
        done
    else
        echo "   (no new migrations)"
    fi
fi

echo ""
echo "🔄 Starting rebase..."
echo ""

# Perform the rebase
if git rebase "origin/$TARGET_BRANCH"; then
    echo ""
    echo "✅ Rebase completed successfully"
    
    # The post-rebase hook should have run automatically
    # But let's check if there are any unstaged migration changes
    if [ -d "$MIGRATION_DIR" ]; then
        MIGRATION_CHANGES=$(git status --short "$MIGRATION_DIR/" 2>/dev/null || true)
        
        if [ -n "$MIGRATION_CHANGES" ]; then
            echo ""
            echo "📋 Migration changes detected:"
            echo "$MIGRATION_CHANGES"
            echo ""
            echo "ℹ️  These changes were made by the post-rebase hook to fix conflicts."
            echo ""
            read -p "Review the changes above. Add and amend commit? (y/N) " -n 1 -r
            echo
            if [[ $REPLY =~ ^[Yy]$ ]]; then
                git add "$MIGRATION_DIR/"
                git commit --amend --no-edit
                echo "✅ Changes committed"
                echo ""
                echo "To push your rebased branch:"
                echo "   git push --force-with-lease"
            else
                echo "ℹ️  Changes left unstaged. Review and commit manually."
            fi
        fi
    fi
else
    echo ""
    echo "❌ Rebase failed or has conflicts"
    echo ""
    echo "After resolving conflicts, continue with:"
    echo "   git rebase --continue"
    echo ""
    echo "The post-rebase hook will check migrations after the rebase completes."
    exit 1
fi
