#!/bin/bash
# Port information and process discovery
#
# Usage: ./.reliant/tools/port-info.sh
# Usage: ./.reliant/tools/port-info.sh check PORT
# Usage: ./.reliant/tools/port-info.sh find-free [START_PORT]

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

check_port() {
  PORT=$1
  PID=$(lsof -ti tcp:$PORT 2>/dev/null || echo "")
  if [ -n "$PID" ]; then
    PROCESS=$(ps -p $PID -o comm= 2>/dev/null || echo "unknown")
    echo "  Port $PORT: IN USE by $PROCESS (PID: $PID)"
    return 0
  else
    echo "  Port $PORT: FREE"
    return 1
  fi
}

case "$1" in
  check)
    if [ -z "$2" ]; then
      echo "Usage: $0 check PORT"
      exit 1
    fi
    check_port "$2"
    ;;
    
  find-free)
    START=${2:-8000}
    echo "Finding free port starting from $START..."
    for PORT in $(seq $START $((START + 100))); do
      if ! lsof -ti tcp:$PORT >/dev/null 2>&1; then
        echo "Found free port: $PORT"
        exit 0
      fi
    done
    echo "No free port found in range $START-$((START + 100))"
    exit 1
    ;;
    
  ""|show)
    echo "=============================="
    echo "Reliant Port Configuration"
    echo "=============================="
    echo ""
    
    # Load and display configured ports
    if [ -f "$PROJECT_ROOT/.env.ports" ]; then
      echo "Configured Ports (.env.ports):"
      source "$PROJECT_ROOT/.env.ports"
      
      check_port "${FRONTEND_PORT:-5173}"
      check_port "${BACKEND_PORT:-8080}"
      check_port "${GRPC_PORT:-9090}"
      check_port "${TEMPORAL_FRONTEND_PORT:-7233}"
      check_port "${TEMPORAL_UI_PORT:-8233}"
      
    elif [ -f "$PROJECT_ROOT/.ports.json" ]; then
      echo "Configured Ports (.ports.json):"
      cat "$PROJECT_ROOT/.ports.json" | jq -r 'to_entries | .[] | "  \(.key): \(.value)"'
    else
      echo "No port configuration found."
      echo "Start dev server to generate .env.ports"
    fi
    
    echo ""
    echo "=============================="
    ;;
    
  help|--help|-h)
    echo "Port Information Tool"
    echo ""
    echo "Usage: $0 [COMMAND] [ARGS]"
    echo ""
    echo "Commands:"
    echo "  show (default)    Show configured ports and their status"
    echo "  check PORT        Check if a specific port is in use"
    echo "  find-free [PORT]  Find a free port starting from PORT (default: 8000)"
    ;;
    
  *)
    echo "Unknown command: $1"
    echo "Run '$0 help' for usage."
    exit 1
    ;;
esac
