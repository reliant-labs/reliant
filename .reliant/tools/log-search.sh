#!/usr/bin/env bash
# Search the dev stack's logs: one file per workload under .forge/logs/dev/.
#
#   ./.reliant/tools/log-search.sh PATTERN [GREP_OPTIONS]   every log, last 30 hits each
#   ./.reliant/tools/log-search.sh --cp PATTERN             control-plane's stack instead
#   ./.reliant/tools/log-search.sh --errors                 error/panic/fatal lines + [browser:error]
#   ./.reliant/tools/log-search.sh --tail [N] [WORKLOAD]    last N lines (default 50, every log)
#   ./.reliant/tools/log-search.sh --where                  which logs exist, and when each was written
#
# This checkout's stack writes reliant-api-server.log, reliant-temporal-worker.log,
# daemon-gateway.log, reliant-migrate.log and frontend_web.log (the browser
# console, as [browser:<level>] lines). control-plane's adds admin-server.log,
# frontend_reliant-web.log, reliant-electron-main.log and more. A zero-hit grep
# proves nothing until the file is live: `--where` shows each file's mtime (the
# dev-logs skill has the full check).
#
# The log directory is fixed by forge, so this needs no running stack.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
. "$here/stack.sh"
stack_args "$@"; set -- "${STACK_REST[@]+"${STACK_REST[@]}"}"
case "$STACK_SELECT" in
  cp|control-plane) STACK_DIR="$(cd "${CONTROL_PLANE_DIR:-$STACK_ROOT/../control-plane}" && pwd)" ;;
  *) STACK_DIR=$STACK_ROOT ;;
esac
dir="$STACK_DIR/.forge/logs/dev"

logs() { ls "$dir"/*.log 2>/dev/null || true; }
[ -n "$(logs)" ] || [ "${1:-}" = --help ] || [ "${1:-}" = -h ] || {
  echo "log-search: no logs under $dir (start the stack: forge env up dev${STACK_SELECT:+ in $STACK_DIR})" >&2
  exit 1
}

case "${1:-}" in
  --where)
    echo "$dir"
    for f in $(logs); do printf '  %-36s %8s lines  written %s\n' "$(basename "$f")" "$(wc -l <"$f" | tr -d ' ')" "$(date -r "$f" '+%F %T')"; done
    echo "  now $(date '+%F %T')" ;;
  --tail)
    n=${2:-50}; w=${3:-}
    for f in $(logs); do
      [ -z "$w" ] || [ "$(basename "$f" .log)" = "$w" ] || continue
      echo "--- $(basename "$f") ---"; tail -n "$n" "$f"
    done ;;
  --errors)
    for f in $(logs); do
      hits=$(grep -nE '\[browser:error\]|level=(ERROR|error)|"level":"(error|ERROR)"|panic:|fatal error|FATAL' "$f" | tail -30 || true)
      [ -n "$hits" ] && { echo "--- $(basename "$f") ---"; echo "$hits"; }
    done; true ;;
  ""|--help|-h) sed -n '2,18p' "$0" ;;
  *)
    pattern=$1; shift
    for f in $(logs); do
      hits=$(grep -n "$@" -- "$pattern" "$f" | tail -30 || true)
      [ -n "$hits" ] && { echo "--- $(basename "$f") ---"; echo "$hits"; }
    done; true ;;
esac
