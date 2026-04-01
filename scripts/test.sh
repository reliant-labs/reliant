#!/bin/bash
# Copyright (c) 2025 Reliant Labs. All rights reserved.

# Script to run V2 backend tests with various options

set -e  # Exit on error

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BLUE}  V2 Backend Test Runner${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""

# Default options
VERBOSE=false
RACE=false
COVERAGE=false
BENCH=false
UNIT_ONLY=false
E2E_ONLY=false
WORKFLOW_ONLY=false
SPECIFIC_TEST=""

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -v|--verbose)
            VERBOSE=true
            shift
            ;;
        -r|--race)
            RACE=true
            shift
            ;;
        -c|--coverage)
            COVERAGE=true
            shift
            ;;
        -b|--bench)
            BENCH=true
            shift
            ;;
        --unit)
            UNIT_ONLY=true
            shift
            ;;
        --e2e)
            E2E_ONLY=true
            shift
            ;;
        --workflow)
            WORKFLOW_ONLY=true
            shift
            ;;
        -t|--test)
            SPECIFIC_TEST="$2"
            shift 2
            ;;
        -h|--help)
            echo "Usage: $0 [options]"
            echo ""
            echo "Options:"
            echo "  -v, --verbose      Run tests in verbose mode"
            echo "  -r, --race         Run with race detector"
            echo "  -c, --coverage     Generate coverage report"
            echo "  -b, --bench        Run benchmarks"
            echo "  --unit             Run unit tests only"
            echo "  --e2e              Run E2E tests only"
            echo "  --workflow         Run workflow integration tests only"
            echo "  -t, --test NAME    Run specific test by name"
            echo "  -h, --help         Show this help message"
            echo ""
            echo "Examples:"
            echo "  $0                                    # Run all tests"
            echo "  $0 -v -r                              # Run with verbose and race detector"
            echo "  $0 -c                                 # Run with coverage report"
            echo "  $0 -b                                 # Run benchmarks"
            echo "  $0 --unit                             # Run unit tests only"
            echo "  $0 --e2e                              # Run E2E tests only"
            echo "  $0 -t TestE2EBasicChatFlow            # Run specific test"
            echo ""
            exit 0
            ;;
        *)
            echo -e "${RED}Unknown option: $1${NC}"
            echo "Use -h or --help for usage information"
            exit 1
            ;;
    esac
done

# Build test flags
TEST_FLAGS="-timeout 5m"

if [ "$VERBOSE" = true ]; then
    TEST_FLAGS="$TEST_FLAGS -v"
fi

if [ "$RACE" = true ]; then
    TEST_FLAGS="$TEST_FLAGS -race"
    echo -e "${YELLOW}⚠️  Running with race detector (slower execution)${NC}"
fi

if [ "$COVERAGE" = true ]; then
    TEST_FLAGS="$TEST_FLAGS -coverprofile=coverage.out -covermode=atomic"
fi

if [ -n "$SPECIFIC_TEST" ]; then
    TEST_FLAGS="$TEST_FLAGS -run $SPECIFIC_TEST"
fi

# Determine which tests to run
if [ "$BENCH" = true ]; then
    echo -e "${BLUE}📊 Running benchmarks...${NC}"
    echo ""
    go test ./tests/integration/... -bench=. -benchmem -run=^$ $TEST_FLAGS
    echo ""
    echo -e "${GREEN}✅ Benchmarks completed${NC}"
    exit 0
fi

echo -e "${BLUE}🧪 Running V2 Backend Tests${NC}"
echo ""

if [ "$UNIT_ONLY" = true ]; then
    echo -e "${YELLOW}Running unit tests only...${NC}"
    go test ./internal/v2/api/handlers/... $TEST_FLAGS
elif [ "$E2E_ONLY" = true ]; then
    echo -e "${YELLOW}Running E2E tests only...${NC}"
    go test ./tests/integration/... $TEST_FLAGS -run "TestE2E"
elif [ "$WORKFLOW_ONLY" = true ]; then
    echo -e "${YELLOW}Running workflow integration tests only...${NC}"
    go test ./tests/integration/... $TEST_FLAGS -run "TestWorkflow"
elif [ -n "$SPECIFIC_TEST" ]; then
    echo -e "${YELLOW}Running specific test: $SPECIFIC_TEST${NC}"
    go test ./tests/integration/... ./internal/v2/api/handlers/... $TEST_FLAGS
else
    echo -e "${YELLOW}Running all V2 tests...${NC}"
    echo ""

    echo -e "${BLUE}1. Unit Tests${NC}"
    go test ./internal/v2/api/handlers/... $TEST_FLAGS
    echo ""

    echo -e "${BLUE}2. E2E Tests${NC}"
    go test ./tests/integration/... $TEST_FLAGS
    echo ""
fi

# Generate coverage report if requested
if [ "$COVERAGE" = true ]; then
    echo ""
    echo -e "${BLUE}📈 Generating coverage report...${NC}"

    if [ -f coverage.out ]; then
        # Generate HTML report
        go tool cover -html=coverage.out -o coverage.html

        # Show coverage summary
        echo ""
        go tool cover -func=coverage.out | tail -n 1

        echo ""
        echo -e "${GREEN}✅ Coverage report generated:${NC}"
        echo -e "   - coverage.out (raw data)"
        echo -e "   - coverage.html (visual report)"
        echo ""
        echo -e "${YELLOW}View coverage report:${NC}"
        echo -e "   open coverage.html"
    else
        echo -e "${RED}❌ Coverage file not found${NC}"
    fi
fi

echo ""
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${GREEN}  All tests completed successfully! ✅${NC}"
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
