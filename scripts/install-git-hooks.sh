#!/bin/bash
# Install git hooks for migration conflict detection and fixing

set -e

# Get the git directory (handles both regular repos and worktrees)
GIT_DIR=$(git rev-parse --git-common-dir)

if [ ! -d "$GIT_DIR" ]; then
    echo "❌ Could not find git directory"
    exit 1
fi

HOOKS_DIR="$GIT_DIR/hooks"

if [ ! -d "$HOOKS_DIR" ]; then
    mkdir -p "$HOOKS_DIR"
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "🔧 Installing git hooks to: $HOOKS_DIR"

# Install post-rebase hook
POST_REBASE_HOOK="$HOOKS_DIR/post-rebase"

cat > "$POST_REBASE_HOOK" << 'EOF'
#!/bin/bash
# Post-rebase hook - automatically fixes migration conflicts after rebase

MIGRATION_DIR="internal/db/migrations/postgres"

# Get the root of the git repo
REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT"

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
if [ -x "$REPO_ROOT/scripts/fix-migration-conflicts.sh" ]; then
    "$REPO_ROOT/scripts/fix-migration-conflicts.sh"
else
    echo "⚠️  Warning: scripts/fix-migration-conflicts.sh not found or not executable"
fi

exit 0
EOF

chmod +x "$POST_REBASE_HOOK"
echo "✅ Installed: post-rebase"

# Install pre-push hook
PRE_PUSH_HOOK="$HOOKS_DIR/pre-push"

cat > "$PRE_PUSH_HOOK" << 'EOF'
#!/bin/bash
# Pre-push hook - checks for migration conflicts before pushing

MIGRATION_DIR="internal/db/migrations/postgres"

# Get the root of the git repo
REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT"

# Only run if migrations directory exists
if [ ! -d "$MIGRATION_DIR" ]; then
    exit 0
fi

# Get current branch
CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)

# Get default branch name
MAIN_BRANCH=$(git rev-parse --abbrev-ref origin/HEAD 2>/dev/null | sed 's/origin\///' || echo "main")

# Skip if we're pushing to main (conflicts will be caught in CI)
if [ "$CURRENT_BRANCH" = "$MAIN_BRANCH" ]; then
    exit 0
fi

# Fetch latest main to compare (silently)
git fetch origin "$MAIN_BRANCH" --quiet 2>/dev/null || true

if ! git rev-parse --verify "origin/$MAIN_BRANCH" >/dev/null 2>&1; then
    # Can't verify against main, allow push
    exit 0
fi

echo "🔍 Checking for migration conflicts with origin/$MAIN_BRANCH..."

# Get highest migration in origin/main
MAIN_MAX=0
while IFS= read -r file; do
    if [ -n "$file" ]; then
        filename=$(basename "$file")
        number=$(echo "$filename" | grep -oE '^[0-9]+' || echo "0")
        number=$((10#$number))
        if [ "$number" -gt "$MAIN_MAX" ]; then
            MAIN_MAX=$number
        fi
    fi
done < <(git ls-tree -r --name-only "origin/$MAIN_BRANCH" "$MIGRATION_DIR/" 2>/dev/null | grep '\.sql$' || true)

# Get migrations added in current branch
HAS_CONFLICTS=false
while IFS= read -r file; do
    if [ -n "$file" ]; then
        filename=$(basename "$file")
        number=$(echo "$filename" | grep -oE '^[0-9]+' || echo "0")
        number=$((10#$number))
        
        if [ "$number" -le "$MAIN_MAX" ]; then
            # Check if this exact file exists in main
            MAIN_FILE=$(git ls-tree -r --name-only "origin/$MAIN_BRANCH" "$MIGRATION_DIR/$filename" 2>/dev/null || true)
            
            if [ -z "$MAIN_FILE" ]; then
                echo "❌ Migration conflict detected!"
                echo "   Your migration: $filename (number: $number)"
                echo "   Conflicts with: origin/$MAIN_BRANCH has migrations up to $MAIN_MAX"
                echo ""
                echo "To fix, run:"
                echo "   git rebase origin/$MAIN_BRANCH"
                echo "   (The post-rebase hook will auto-fix)"
                echo ""
                echo "Or manually run:"
                echo "   ./scripts/fix-migration-conflicts.sh"
                echo ""
                HAS_CONFLICTS=true
            fi
        fi
    fi
done < <(git diff --name-only --diff-filter=A "origin/$MAIN_BRANCH" HEAD -- "$MIGRATION_DIR/*.sql" 2>/dev/null || true)

if [ "$HAS_CONFLICTS" = true ]; then
    exit 1
fi

echo "✅ No migration conflicts detected"
exit 0
EOF

chmod +x "$PRE_PUSH_HOOK"
echo "✅ Installed: pre-push"

echo ""
echo "✅ Git hooks installed successfully!"
echo ""
echo "Hooks installed:"
echo "  • post-rebase: Auto-fixes migration conflicts after rebase"
echo "  • pre-push: Blocks push if migration conflicts detected"
echo ""
echo "To manually check/fix migrations anytime:"
echo "  ./scripts/fix-migration-conflicts.sh"
