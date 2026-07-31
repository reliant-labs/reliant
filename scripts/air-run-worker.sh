#!/usr/bin/env bash
#
# air-run-worker.sh — idempotent launch wrapper for the air-managed Temporal
# worker under `forge up --env=dev`.
#
# WHY THIS EXISTS
# ---------------
# air rebuilds the worker on every .go change. The intent is: SIGINT the old
# worker, wait for it to drain, then start the new one. In practice the OLD
# worker keeps leaking and has to be reaped by hand. Root cause is in air's
# runner (github.com/air-verse/air@v1.64.5):
#
#   * runner/engine.go:780-804  stopBin() caps its wait on the old process at a
#     HARDCODED 5s (`case <-time.After(5 * time.Second)`), independent of our
#     kill_delay="30s". When the drain runs long, air stops waiting and moves on.
#   * runner/engine.go:83-85    eventCh is buffered 1000 and buildRunCh is a
#     capacity-1 semaphore; a burst of file writes (exactly what a forge
#     one-shot run produces) drives overlapping build/run cycles.
#   * runner/engine.go:728-758  a freshly-started worker is only wired into air's
#     kill tracking (`e.binStopCh = killFunc(...)`, line 751) AFTER startCmd
#     (line 735). Under a burst the next event's stopBin() can fire against a
#     stale/nil binStopCh, orphaning a live worker: its killFunc goroutine parks
#     forever on `shutdown` (it never comes) and only wakes on `processExit`
#     (which never comes, because nothing signalled it). air never sends it
#     SIGINT.
#
# The worker log confirms it: every SIGINT the worker DOES receive drains in
# milliseconds (zero "Worker stop timed out" ever), yet stale workers survive
# and get cleaned up with a manual `kill` (which arrives as SIGTERM). So the
# leaked workers are not stuck draining — they simply never receive air's
# SIGINT. Because every worker shares a stable Temporal BuildID
# ("reliant-worker") on the shared "reliant-workflows" task queue, an orphaned
# old-code worker keeps polling and serving stale tasks that then fail.
#
# WHAT THIS DOES
# --------------
# Makes worker startup idempotent: at most one worker is ever alive. Before
# exec'ing the freshly-built binary, reap the worker recorded in the pidfile if
# it is still running (a graceful SIGTERM, bounded wait, then SIGKILL). Then
# record our own PID and exec the real binary in place — so the worker inherits
# this wrapper's PID and process group and air's own SIGINT/SIGKILL still reach
# it directly, with no extra process layer and no signal forwarding needed.
#
# This is a dev-only backstop for air's tracking races. It deliberately does
# NOT touch the Temporal BuildID / worker-versioning setup: plain BuildID is
# just a label and does not fence task dispatch unless full Worker Versioning is
# enabled on the queue, and enabling that would change production workflow
# routing/determinism — the wrong layer for a local hot-reload leak.
#
# air invokes this as:  scripts/air-run-worker.sh <args_bin...>   (e.g. server worker)
# from the repo root. Activate by pointing `.air.worker.toml` `full_bin` at it.

# No `set -e`: reaping is strictly best-effort and must NEVER prevent the exec
# of the new worker. Every reap step is guarded with `|| true`.
set -uo pipefail

REAL_BIN="${RELIANT_WORKER_BIN:-./tmp/air-worker/reliant}"
PIDFILE="${RELIANT_WORKER_PIDFILE:-tmp/air-worker/worker.pid}"
# Fragment used to positively identify a worker process before signalling it,
# so a recycled PID that now belongs to some unrelated process is never killed.
MATCH="${REAL_BIN#./}"

reap_prior_worker() {
  [[ -f "$PIDFILE" ]] || return 0

  local old_pid
  old_pid="$(cat "$PIDFILE" 2>/dev/null || true)"
  [[ -n "${old_pid}" ]] || return 0
  # Not a live PID → nothing to reap (the common, healthy case: air already
  # SIGINT'd it and it exited).
  kill -0 "$old_pid" 2>/dev/null || return 0

  # Confirm the PID is actually our worker binary before touching it.
  if ! ps -p "$old_pid" -o args= 2>/dev/null | grep -Fq -- "$MATCH"; then
    return 0
  fi

  echo "air-run-worker: reaping leaked prior worker pid=$old_pid" >&2
  kill -TERM "$old_pid" 2>/dev/null || true

  # Bounded graceful wait (~5s). Normal drains finish well under 1s.
  local i
  for ((i = 0; i < 50; i++)); do
    kill -0 "$old_pid" 2>/dev/null || return 0
    sleep 0.1
  done

  # Still alive after the grace window — force it.
  echo "air-run-worker: prior worker pid=$old_pid did not exit; sending SIGKILL" >&2
  kill -KILL "$old_pid" 2>/dev/null || true
}

reap_prior_worker

# Record our PID; after the exec below the worker binary keeps this same PID,
# so the NEXT rebuild's wrapper can reap us if air's own tracking drops us.
echo "$$" > "$PIDFILE" 2>/dev/null || true

# Replace this shell with the real worker (same PID / process group).
exec "$REAL_BIN" "$@"
