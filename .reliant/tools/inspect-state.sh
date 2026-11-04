#!/usr/bin/env bash
# One look at the running dev stack: health of each server role, the web app,
# the database's chats and workflows, and the logs.
#
#   ./.reliant/tools/inspect-state.sh          this checkout's `forge env up dev`
#   ./.reliant/tools/inspect-state.sh --cp     control-plane's full-platform stack
#   ./.reliant/tools/inspect-state.sh --full   also the newest chats and git status
#
# Everything is read from `forge env config dev` (stack.sh). The database is
# queried READ-ONLY: control-plane's (localhost:5434) holds real data.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
. "$here/stack.sh"
stack_args "$@"; set -- "${STACK_REST[@]+"${STACK_REST[@]}"}"
full=false; [ "${1:-}" = --full ] && full=true

stack_load
echo "== $STACK_NAME dev stack ($STACK_DIR)"
echo

probe() {
  local name=$1 url=$2 code
  [ -n "$url" ] || { printf '  %-16s (not declared)\n' "$name"; return; }
  code=$(curl -s --connect-timeout 2 -o /dev/null -w '%{http_code}' "$url" 2>/dev/null || true)
  case "$code" in 2*|3*) printf '  %-16s up    %s\n' "$name" "$url" ;; *) printf '  %-16s DOWN  %s (HTTP %s)\n' "$name" "$url" "${code:-none}" ;; esac
}
echo "-- health"
probe "api /ready" "${API_HEALTH_URL:+$API_HEALTH_URL/ready}"
probe "worker /ready" "${WORKER_HEALTH_URL:+$WORKER_HEALTH_URL/ready}"
probe "gateway /ready" "${GATEWAY_HEALTH_URL:+$GATEWAY_HEALTH_URL/ready}"
probe "web" "$WEB_URL"
echo

echo "-- database (read-only)"
if [ -z "$DATABASE_URL" ]; then
  echo "  (no DATABASE_URL declared)"
elif ! command -v psql >/dev/null 2>&1; then
  echo "  $DATABASE_URL  (psql not installed)"
else
  echo "  $DATABASE_URL"
  # chats.state is the reliant.v1.ChatState number: 3 is CHAT_STATE_ARCHIVED.
  q() { PGOPTIONS='-c default_transaction_read_only=on' psql "$DATABASE_URL" -XAtq -c "$1" 2>/dev/null || echo "?"; }
  echo "  chats:     $(q "SELECT count(*) FROM chats") ($(q "SELECT count(*) FROM chats WHERE state IS DISTINCT FROM 3") not archived)"
  echo "  messages:  $(q "SELECT count(*) FROM messages")"
  echo "  workflows: $(q "SELECT count(*) FROM workflows")"
  if $full; then
    echo "  newest chats:"
    PGOPTIONS='-c default_transaction_read_only=on' psql "$DATABASE_URL" -Xq -c \
      "SELECT id, left(title, 40) AS title, state, worktree_id IS NOT NULL AS branched, last_active FROM chats ORDER BY last_active DESC LIMIT 5" 2>/dev/null | sed 's/^/    /' || echo "    (query failed)"
  fi
fi
echo

echo "-- logs"
"$here/log-search.sh" $([ "$STACK_NAME" = control-plane ] && echo --cp) --where 2>&1 | sed 's/^/  /' || true
errors=$("$here/log-search.sh" $([ "$STACK_NAME" = control-plane ] && echo --cp) --errors 2>/dev/null | grep -vc '^---' || true)
echo "  error-looking lines (last 30 per log): ${errors:-0}  (log-search.sh --errors)"

if $full; then
  echo
  echo "-- git"
  git -C "$STACK_ROOT" status --short | head -10
fi
