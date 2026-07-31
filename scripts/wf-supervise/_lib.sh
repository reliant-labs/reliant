# shellcheck shell=bash
# Shared helpers for wf-supervise scripts. Source only — not executable.
#
# READ-ONLY BY CONSTRUCTION: every connection sets
# default_transaction_read_only=on, so even a bug in a query cannot write.

WF_DB_URL="${RELIANT_DB_URL:-postgres://postgres:postgres@127.0.0.1:5434/reliant}"

# run_sql [psql-args...] — read-only psql against the reliant dev DB.
run_sql() {
  PGOPTIONS="-c default_transaction_read_only=on" \
    psql "$WF_DB_URL" -X -q -v ON_ERROR_STOP=1 "$@"
}

# SQL fragment: map workflows.status int -> name.
# 1 pending, 2 running, 3 completed, 4 failed, 5 cancelled, 6 paused, 7 expired
WF_STATUS_SQL="CASE %s
  WHEN 1 THEN 'pending' WHEN 2 THEN 'running' WHEN 3 THEN 'completed'
  WHEN 4 THEN 'failed' WHEN 5 THEN 'cancelled' WHEN 6 THEN 'paused'
  WHEN 7 THEN 'expired' ELSE 'status='||%s END"

wf_status_expr() { # $1 = column ref
  # shellcheck disable=SC2059
  printf "$WF_STATUS_SQL" "$1" "$1"
}

# resolve_execution_id <id-or-prefix> — resolve possibly-abbreviated workflow id.
# Prints the full id, or exits 1 with an error on stderr.
resolve_execution_id() {
  local arg="$1" matches
  matches=$(run_sql -At -v pfx="$arg" <<'SQL'
SELECT id FROM workflows WHERE id = :'pfx'
UNION
SELECT id FROM workflows WHERE id LIKE :'pfx' || '%'
LIMIT 5;
SQL
  ) || return 1
  local count
  count=$(printf '%s' "$matches" | grep -c . || true)
  if [ "$count" -eq 0 ]; then
    echo "error: no workflow execution matching '$arg'" >&2
    return 1
  elif [ "$count" -gt 1 ]; then
    echo "error: ambiguous execution id '$arg', matches:" >&2
    printf '%s\n' "$matches" >&2
    return 1
  fi
  printf '%s\n' "$matches"
}
