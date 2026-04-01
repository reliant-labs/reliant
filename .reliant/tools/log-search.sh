#!/bin/bash
# Log search helper for Reliant development
#
# Usage: ./.reliant/tools/log-search.sh "error"
# Usage: ./.reliant/tools/log-search.sh "workflow" -A5 -B2
# Usage: ./.reliant/tools/log-search.sh "panic" --all
# Usage: ./.reliant/tools/log-search.sh --tail 50
# Usage: ./.reliant/tools/log-search.sh --recent

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

LOG_FILE="$PROJECT_ROOT/data/logs.txt"
BUILD_LOG="$PROJECT_ROOT/data/build-errors.log"
RELIANT_LOG="$PROJECT_ROOT/data/logs/reliant.log"

# Check which log files exist
check_logs() {
  local found=0
  [ -f "$LOG_FILE" ] && found=1 && echo "  Main log:  $LOG_FILE ($(wc -l < "$LOG_FILE" | tr -d ' ') lines)"
  [ -f "$RELIANT_LOG" ] && found=1 && echo "  App log:   $RELIANT_LOG ($(wc -l < "$RELIANT_LOG" | tr -d ' ') lines)"
  [ -f "$BUILD_LOG" ] && found=1 && echo "  Build log: $BUILD_LOG ($(wc -l < "$BUILD_LOG" 2>/dev/null | tr -d ' ') lines)"
  return $found
}

case "$1" in
  --tail)
    LINES="${2:-50}"
    echo "=== Last $LINES lines of logs ==="
    if [ -f "$LOG_FILE" ]; then
      echo "--- $LOG_FILE ---"
      tail -n "$LINES" "$LOG_FILE"
    fi
    ;;
    
  --recent)
    echo "=== Last 5 minutes of activity ==="
    if [ -f "$LOG_FILE" ]; then
      # Show last 100 lines by default for recent
      tail -n 100 "$LOG_FILE"
    fi
    ;;
    
  --errors)
    echo "=== Errors and Panics ==="
    if [ -f "$LOG_FILE" ]; then
      grep -iE "(error|panic|fatal|fail)" "$LOG_FILE" | tail -50
    fi
    if [ -f "$BUILD_LOG" ] && [ -s "$BUILD_LOG" ]; then
      echo ""
      echo "--- Build Errors ---"
      cat "$BUILD_LOG"
    fi
    ;;
    
  --build)
    echo "=== Build Errors ==="
    if [ -f "$BUILD_LOG" ] && [ -s "$BUILD_LOG" ]; then
      cat "$BUILD_LOG"
    else
      echo "No build errors found."
    fi
    ;;
    
  --all)
    shift
    PATTERN="$1"
    shift
    echo "=== Searching all logs for: $PATTERN ==="
    for log in "$LOG_FILE" "$RELIANT_LOG" "$BUILD_LOG"; do
      if [ -f "$log" ]; then
        echo "--- $log ---"
        grep -n "$@" "$PATTERN" "$log" 2>/dev/null | tail -30 || echo "(no matches)"
        echo ""
      fi
    done
    ;;
    
  --where)
    echo "=== Log File Locations ==="
    check_logs || echo "  No log files found. Start dev server first."
    ;;
    
  help|--help|-h)
    echo "Log Search Helper"
    echo ""
    echo "Usage: $0 [OPTIONS] [PATTERN] [GREP_OPTIONS]"
    echo ""
    echo "Options:"
    echo "  --tail [N]      Show last N lines (default: 50)"
    echo "  --recent        Show last ~100 lines"
    echo "  --errors        Search for errors/panics"
    echo "  --build         Show build errors only"
    echo "  --all PATTERN   Search all log files"
    echo "  --where         Show log file locations"
    echo ""
    echo "Pattern Search:"
    echo "  $0 \"error\"                Search main log"
    echo "  $0 \"workflow\" -A5 -B2     With context lines"
    echo "  $0 \"panic\" -i             Case insensitive"
    echo ""
    echo "Log Files:"
    check_logs || echo "  (start dev server to create logs)"
    ;;
    
  "")
    echo "Usage: $0 PATTERN [OPTIONS]"
    echo "Run '$0 --help' for more information."
    exit 1
    ;;
    
  *)
    PATTERN="$1"
    shift
    
    if [ ! -f "$LOG_FILE" ]; then
      echo "Error: Main log file not found at $LOG_FILE"
      echo "Start dev server first: ./scripts/dev.sh"
      exit 1
    fi
    
    echo "=== Searching for: $PATTERN ==="
    grep -n "$@" "$PATTERN" "$LOG_FILE" | tail -50
    ;;
esac
