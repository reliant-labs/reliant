#!/bin/bash

# Helper script to tail development logs

LOG_FILE="./data//logs.txt"
BUILD_LOG="./data/build-errors.log"

show_help() {
    echo "Usage: npm run logs [OPTIONS]"
    echo ""
    echo "Watch development logs in real-time"
    echo ""
    echo "Options:"
    echo "  (no args)      Watch all logs (./data/logs.txt)"
    echo "  --build, -b    Watch build errors only (./data/build-errors.log)"
    echo "  --help, -h     Show this help message"
    echo ""
    echo "Examples:"
    echo "  npm run logs           # Watch all logs"
    echo "  npm run logs -- -b     # Watch build errors only"
}

# Parse arguments
case "${1:-}" in
    --build|-b)
        if [ ! -f "$BUILD_LOG" ]; then
            echo "Build log not found: $BUILD_LOG"
            echo "Start development with 'npm run dev' first"
            exit 1
        fi
        echo "Watching build errors: $BUILD_LOG"
        echo "Press Ctrl-C to stop"
        echo ""
        tail -f "$BUILD_LOG"
        ;;
    --help|-h)
        show_help
        exit 0
        ;;
    "")
        if [ ! -f "$LOG_FILE" ]; then
            echo "Log file not found: $LOG_FILE"
            echo "Start development with 'npm run dev' first"
            exit 1
        fi
        echo "Watching all logs: $LOG_FILE"
        echo "Press Ctrl-C to stop"
        echo ""
        tail -f "$LOG_FILE"
        ;;
    *)
        echo "Unknown option: $1"
        echo ""
        show_help
        exit 1
        ;;
esac
