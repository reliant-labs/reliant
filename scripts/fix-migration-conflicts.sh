#!/usr/bin/env bash
# Auto-fix migration number conflicts after rebase/merge
# This script detects migrations that conflict with existing numbers
# and automatically renumbers them to the next available slot
# Compatible with bash 3.2+ (macOS default)

set -e

MIGRATION_DIR="internal/db/migrations/sqlite"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}🔍 Checking for migration conflicts...${NC}"

if [ ! -d "$MIGRATION_DIR" ]; then
    echo -e "${RED}❌ Migration directory not found: $MIGRATION_DIR${NC}"
    exit 1
fi

# Get main branch name
MAIN_BRANCH=$(git rev-parse --abbrev-ref origin/HEAD 2>/dev/null | sed 's/origin\///' || echo "main")

# Check if we can access origin/main
if ! git rev-parse --verify "origin/$MAIN_BRANCH" >/dev/null 2>&1; then
    echo -e "${YELLOW}⚠️  Cannot access origin/$MAIN_BRANCH${NC}"
    echo -e "${YELLOW}   Run: git fetch origin $MAIN_BRANCH${NC}"
    exit 0
fi

echo -e "${BLUE}   Comparing against: origin/$MAIN_BRANCH${NC}"

# Get highest migration number in origin/main
MAIN_MAX=0
while IFS= read -r file; do
    if [ -n "$file" ]; then
        filename=$(basename "$file")
        number=$(echo "$filename" | grep -oE '^[0-9]+' || echo "0")
        # Remove leading zeros
        number=$((10#$number))
        if [ "$number" -gt "$MAIN_MAX" ]; then
            MAIN_MAX=$number
        fi
    fi
done < <(git ls-tree -r --name-only "origin/$MAIN_BRANCH" "$MIGRATION_DIR/" 2>/dev/null | grep '\.sql$' || true)

echo -e "${BLUE}   Highest migration in origin/$MAIN_BRANCH: $MAIN_MAX${NC}"

# Get migrations added in current branch (not in origin/main)
BRANCH_MIGRATIONS=$(mktemp)
git diff --name-only --diff-filter=A "origin/$MAIN_BRANCH" HEAD -- "$MIGRATION_DIR/*.sql" 2>/dev/null | sort > "$BRANCH_MIGRATIONS" || true

# Also check for renamed/modified migrations with conflicting numbers
MODIFIED_MIGRATIONS=$(mktemp)
git diff --name-only --diff-filter=MR "origin/$MAIN_BRANCH" HEAD -- "$MIGRATION_DIR/*.sql" 2>/dev/null | sort > "$MODIFIED_MIGRATIONS" || true

# Check if we have any new migrations
if [ ! -s "$BRANCH_MIGRATIONS" ] && [ ! -s "$MODIFIED_MIGRATIONS" ]; then
    echo -e "${GREEN}✅ No new or modified migrations in current branch${NC}"
    rm -f "$BRANCH_MIGRATIONS" "$MODIFIED_MIGRATIONS"
    exit 0
fi

echo -e "${BLUE}📋 Migrations in your branch:${NC}"

# Collect migrations that need fixing
MIGRATIONS_TO_FIX=$(mktemp)
CONFLICTS_FOUND=false

# Check added migrations
while IFS= read -r file; do
    if [ -n "$file" ]; then
        filename=$(basename "$file")
        number=$(echo "$filename" | grep -oE '^[0-9]+' || echo "0")
        number=$((10#$number))
        
        echo -e "   ${filename}"
        
        if [ "$number" -le "$MAIN_MAX" ]; then
            echo -e "${RED}      ❌ Conflict: number $number ≤ main's max $MAIN_MAX${NC}"
            echo "$file" >> "$MIGRATIONS_TO_FIX"
            CONFLICTS_FOUND=true
        else
            echo -e "${GREEN}      ✅ OK: number $number > main's max $MAIN_MAX${NC}"
        fi
    fi
done < "$BRANCH_MIGRATIONS"

# Check modified migrations for number conflicts
while IFS= read -r file; do
    if [ -n "$file" ] && [ -f "$file" ]; then
        filename=$(basename "$file")
        number=$(echo "$filename" | grep -oE '^[0-9]+' || echo "0")
        number=$((10#$number))
        
        # Check if a different file with this number exists in main
        MAIN_FILE=$(git ls-tree -r --name-only "origin/$MAIN_BRANCH" "$MIGRATION_DIR/${number}_"* 2>/dev/null | head -1 || true)
        
        if [ -n "$MAIN_FILE" ]; then
            MAIN_BASENAME=$(basename "$MAIN_FILE")
            if [ "$MAIN_BASENAME" != "$filename" ]; then
                echo -e "   ${filename}"
                echo -e "${RED}      ❌ Conflict: different file with number $number exists in main${NC}"
                echo "$file" >> "$MIGRATIONS_TO_FIX"
                CONFLICTS_FOUND=true
            fi
        fi
    fi
done < "$MODIFIED_MIGRATIONS"

if [ "$CONFLICTS_FOUND" = false ]; then
    echo -e "${GREEN}✅ No migration conflicts detected${NC}"
    rm -f "$BRANCH_MIGRATIONS" "$MODIFIED_MIGRATIONS" "$MIGRATIONS_TO_FIX"
    exit 0
fi

# We have conflicts to fix
echo -e ""
echo -e "${YELLOW}🔧 Fixing migration conflicts...${NC}"

# Get current highest number across all local migrations
CURRENT_MAX=$MAIN_MAX
for file in "$MIGRATION_DIR"/*.sql; do
    if [ -f "$file" ]; then
        filename=$(basename "$file")
        number=$(echo "$filename" | grep -oE '^[0-9]+' || echo "0")
        number=$((10#$number))
        if [ "$number" -gt "$CURRENT_MAX" ]; then
            CURRENT_MAX=$number
        fi
    fi
done

# Start renumbering from next available (use +10000 gap to avoid near-collisions)
NEXT_NUMBER=$((CURRENT_MAX + 10000))
FIXED_COUNT=0

# Sort migrations to fix by their current number
sort "$MIGRATIONS_TO_FIX" | while IFS= read -r file; do
    if [ -f "$file" ]; then
        filename=$(basename "$file")
        old_number=$(echo "$filename" | grep -oE '^[0-9]+')
        
        # Detect padding from old number
        PADDING=3
        if [[ $old_number =~ ^0 ]]; then
            PADDING=${#old_number}
        fi
        
        # Create new number with same padding
        new_number=$(printf "%0${PADDING}d" $NEXT_NUMBER)
        new_filename=$(echo "$filename" | sed "s/^${old_number}_/${new_number}_/")
        new_path="$MIGRATION_DIR/$new_filename"
        
        if [ "$file" != "$new_path" ]; then
            echo -e "${GREEN}   ✅ Renumbering: $filename -> $new_filename${NC}"
            
            # Use git mv if file is tracked, otherwise regular mv
            if git ls-files --error-unmatch "$file" >/dev/null 2>&1; then
                git mv "$file" "$new_path"
            else
                mv "$file" "$new_path"
                git add "$new_path"
            fi
            
            FIXED_COUNT=$((FIXED_COUNT + 1))
            NEXT_NUMBER=$((NEXT_NUMBER + 10000))
        fi
    fi
done

# Clean up temp files
rm -f "$BRANCH_MIGRATIONS" "$MODIFIED_MIGRATIONS" "$MIGRATIONS_TO_FIX"

# Check if any files were actually fixed (the subshell issue)
STAGED_CHANGES=$(git diff --cached --name-only "$MIGRATION_DIR/" 2>/dev/null | wc -l | tr -d ' ')

if [ "$STAGED_CHANGES" -gt 0 ]; then
    echo -e ""
    echo -e "${GREEN}✅ Fixed migration conflicts${NC}"
    echo -e "${YELLOW}ℹ️  Changes have been staged. Review and commit when ready.${NC}"
    echo -e ""
    echo -e "${BLUE}📋 Staged changes:${NC}"
    git status --short "$MIGRATION_DIR/"
    echo -e ""
    echo -e "${YELLOW}To commit these changes:${NC}"
    echo -e "   git commit -m 'chore: renumber migrations after rebase'"
    echo -e "   # Or amend to your last commit:"
    echo -e "   git commit --amend --no-edit"
else
    echo -e "${GREEN}✅ No changes needed${NC}"
fi

exit 0
