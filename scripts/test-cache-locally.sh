#!/bin/bash
set -e

# Quick local validation script for cache configuration
# This doesn't test actual caching but validates the paths exist after build

echo "======================================"
echo "🧪 Local Cache Configuration Validator"
echo "======================================"
echo ""

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Counters
CHECKS=0
PASSED=0
WARNINGS=0

check_path() {
    CHECKS=$((CHECKS + 1))
    local path=$1
    local description=$2
    local required=${3:-true}
    
    if [ -d "$path" ] || [ -f "$path" ]; then
        echo -e "${GREEN}✅ $description${NC}"
        echo "   Path: $path"
        if [ -d "$path" ]; then
            echo "   Size: $(du -sh "$path" 2>/dev/null | cut -f1)"
        fi
        PASSED=$((PASSED + 1))
    else
        if [ "$required" = "true" ]; then
            echo -e "${RED}❌ $description${NC}"
            echo "   Path: $path (not found)"
        else
            echo -e "${YELLOW}⚠️  $description (optional)${NC}"
            echo "   Path: $path (not found - will be created on first build)"
            WARNINGS=$((WARNINGS + 1))
            PASSED=$((PASSED + 1))  # Don't count as failure
        fi
    fi
    echo ""
}

echo "Checking required paths..."
echo ""

# Package lock files (must exist)
check_path "web/package-lock.json" "Web package-lock.json exists" true
check_path "electron/package-lock.json" "Electron package-lock.json exists" true
check_path "go.sum" "Go.sum exists" true

echo ""
echo "Checking cache target directories..."
echo ""

# Node modules (should exist if installed)
check_path "web/node_modules" "Web node_modules" false
check_path "electron/node_modules" "Electron node_modules" false

# Electron cache (created on first electron-builder run)
check_path "$HOME/.cache/electron" "Electron cache directory" false
check_path "$HOME/.cache/electron-builder" "Electron-builder cache directory" false

# Build outputs (created after build)
check_path "web/dist" "Web build output" false
check_path "web/.vite" "Vite cache directory" false
check_path "web/node_modules/.vite" "Vite module cache" false

# Go cache
check_path "$HOME/go/pkg/mod" "Go modules cache" false
check_path "$HOME/.cache/go-build" "Go build cache" false

echo ""
echo "======================================"
echo "📊 Validation Summary"
echo "======================================"
echo ""
echo "Checks performed: $CHECKS"
echo -e "${GREEN}Passed: $PASSED${NC}"
echo -e "${YELLOW}Warnings: $WARNINGS${NC}"
echo ""

if [ $PASSED -eq $CHECKS ]; then
    echo -e "${GREEN}✅ All checks passed!${NC}"
    echo ""
    echo "Your local environment is ready for cache testing."
    echo "Cache paths will be created during workflow runs."
else
    echo -e "${RED}❌ Some checks failed${NC}"
    echo ""
    echo "Missing required files. Please ensure:"
    echo "  1. You've run 'npm install' in web/ and electron/"
    echo "  2. Go dependencies are downloaded: 'go mod download'"
    echo "  3. You're in the project root directory"
fi

echo ""
echo "======================================"
echo "🚀 Next Steps"
echo "======================================"
echo ""
echo "1. Commit and push the cache fixes:"
echo "   git add .github/workflows/"
echo "   git commit -m 'fix: optimize GitHub Actions caching'"
echo "   git push"
echo ""
echo "2. Test with the manual workflow:"
echo "   - Go to GitHub Actions tab"
echo "   - Run 'Test Cache Configuration'"
echo "   - Check for cache warnings in Post steps"
echo ""
echo "3. Run again to verify cache hits"
echo ""
echo "See TESTING_CACHE_FIXES.md for detailed instructions"
echo ""

# Validate YAML files
echo "======================================"
echo "📝 YAML Validation"
echo "======================================"
echo ""

if command -v ruby &> /dev/null; then
    for workflow in .github/workflows/*.yml; do
        if [ -f "$workflow" ]; then
            if ruby -ryaml -e "YAML.load_file('$workflow')" 2>/dev/null; then
                echo -e "${GREEN}✅ $(basename "$workflow") is valid YAML${NC}"
            else
                echo -e "${RED}❌ $(basename "$workflow") has YAML syntax errors${NC}"
            fi
        fi
    done
else
    echo -e "${YELLOW}⚠️  Ruby not found, skipping YAML validation${NC}"
    echo "   Install ruby to validate YAML syntax"
fi

echo ""
echo "======================================"
echo "💡 Pro Tips"
echo "======================================"
echo ""
echo "• First workflow run will populate caches (slower)"
echo "• Second run should be 50-70% faster (cache hits)"
echo "• Look for 'Cache restored from key' in logs"
echo "• Check 'Post cache...' steps for save confirmation"
echo "• No 'Path Validation Error' = success!"
echo ""
