#!/bin/bash
# Script to run all tests locally

set -e

echo "========================================="
echo "Running Complete Test Suite"
echo "========================================="

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Track failures
FAILED=0

# Function to run a test section
run_test_section() {
    local name=$1
    local command=$2
    
    echo -e "\n${YELLOW}Running: $name${NC}"
    echo "----------------------------------------"
    
    if eval "$command"; then
        echo -e "${GREEN}✅ $name passed${NC}"
    else
        echo -e "${RED}❌ $name failed${NC}"
        FAILED=$((FAILED + 1))
    fi
}

# Go Tests
run_test_section "Go Lint" "golangci-lint run"
run_test_section "Go Unit Tests" "go test -v -race ./..."
run_test_section "Go E2E Tests" "go test -v ./test/e2e"
run_test_section "Go Integration Tests" "go test -v ./test/integration/..."

# Web Tests
run_test_section "Web Dependencies" "cd web && npm ci"
run_test_section "Web Lint" "cd web && npm run lint"
run_test_section "Web Type Check" "cd web && npx tsc --noEmit"
run_test_section "Web Unit Tests" "cd web && npm test -- --run"

# Summary
echo ""
echo "========================================="
if [ $FAILED -eq 0 ]; then
    echo -e "${GREEN}✅ All tests passed!${NC}"
    echo "========================================="
    exit 0
else
    echo -e "${RED}❌ $FAILED test section(s) failed${NC}"
    echo "========================================="
    exit 1
fi