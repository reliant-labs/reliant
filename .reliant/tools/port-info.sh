#!/usr/bin/env bash
# Where the dev stack listens, and whether each port is up.
#
#   ./.reliant/tools/port-info.sh             this checkout's `forge env up dev`
#   ./.reliant/tools/port-info.sh --cp        control-plane's full-platform stack
#   ./.reliant/tools/port-info.sh check PORT  what holds one port
#
# Ports come from `forge env config dev` (stack.sh): each worktree's stack has
# its own 100-port block, so never assume a number. `forge env devstack list`
# shows every block on the machine.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/stack.sh"
stack_args "$@"; set -- "${STACK_REST[@]+"${STACK_REST[@]}"}"

check_port() {
  local name=$1 port=$2 pid proc
  [ -n "$port" ] || { printf '  %-18s (not declared by this stack)\n' "$name"; return; }
  pid=$(lsof -ti "tcp:$port" -sTCP:LISTEN 2>/dev/null | head -1 || true)
  if [ -n "$pid" ]; then
    proc=$(ps -p "$pid" -o comm= 2>/dev/null || echo unknown)
    printf '  %-18s %-6s listening (%s, pid %s)\n' "$name" "$port" "$proc" "$pid"
  else
    printf '  %-18s %-6s not listening\n' "$name" "$port"
  fi
}

case "${1:-show}" in
  check)
    [ -n "${2:-}" ] || { echo "usage: $0 check PORT" >&2; exit 2; }
    check_port "port" "$2" ;;
  show)
    stack_load
    echo "$STACK_NAME dev stack ($STACK_DIR)"
    check_port "api (Connect)" "$(stack_port "$API_URL")"
    check_port "api health" "$(stack_port "$API_HEALTH_URL")"
    check_port "worker health" "$(stack_port "$WORKER_HEALTH_URL")"
    check_port "gateway" "$(stack_port "$GATEWAY_URL")"
    check_port "web" "$(stack_port "$WEB_URL")"
    check_port "temporal" "${TEMPORAL_ADDR##*:}"
    check_port "postgres" "$(sed -n 's#.*@[^:/]*:\([0-9]*\)/.*#\1#p' <<<"$DATABASE_URL")"
    echo
    echo "  api      $API_URL"
    echo "  web      $WEB_URL"
    echo "  database $DATABASE_URL"
    echo "  logs     $STACK_LOG_DIR"
    ;;
  help|--help|-h) sed -n '2,10p' "$0" ;;
  *) echo "unknown command: $1 (show, check PORT)" >&2; exit 2 ;;
esac
