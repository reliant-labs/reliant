#!/usr/bin/env bash
# scripts/dev-electron-supervise.sh — signal-forwarding supervisor for the
# Electron dev chain (`npm run dev` in ../reliant/electron).
#
# WHY THIS EXISTS
#
# go-task does not forward signals to the command it is running. Its
# InterceptInterruptSignals (signals.go) registers a handler for SIGINT/SIGTERM
# (+SIGHUP as of 3.52.0) whose entire body is a log line — "task: Signal
# received" — and the commands themselves run inside an embedded mvdan.cc/sh
# interpreter driven by a context.Background() that nothing ever cancels. Task
# assumes the caller signalled the whole process group, which is exactly what a
# terminal Ctrl-C does, so interactive use looks fine and `kill -TERM <task-pid>`
# looks like a no-op: task logs the signal, keeps running, and the dev chain
# below it never hears about it.
#
# Registering a `trap` directly in the Taskfile command cannot fix that, because
# the embedded interpreter is not a real shell process and never receives the
# signal. `exec` does not help either — that interpreter runs `exec foo` as an
# ordinary subprocess instead of replacing the process image, so there is no way
# to hoist the dev chain up onto task's own pid.
#
# This script is a REAL bash process, so it does get signalled, and it is the
# one layer in the chain that can act on it.
#
# HOW IT WORKS
#
# `set -m` (job control) puts the npm chain into its OWN process group, distinct
# from the group task shares with its caller. That gives us a single handle —
# the child's pgid — that covers every layer below us (npm → dev-multi →
# concurrently → wait-and-start → Electron → tools-daemon), so one group signal
# reaches all of them without us having to walk the tree.
#
# Two independent paths then tear that group down:
#
#   1. TRAP — for a signal that reaches us: a group signal (what
#      `task dev-electron-stop` sends), or a terminal Ctrl-C.
#
#   2. WATCHDOG — for a signal that does NOT reach us, which is the case this
#      script exists for. `kill -9 <task-pid>` cannot be caught or forwarded by
#      anyone; task dies instantly and we are reparented to init. Polling
#      `kill -0 $PPID` notices that our parent vanished and tears the group down
#      ourselves. This also covers `kill -TERM <task-pid>` repeated three times,
#      where task's maxInterruptSignals counter finally calls os.Exit(1).
#
# SIGTERM ONLY, NEVER SIGKILL. Electron's gracefulShutdown() is what stops the
# tools-daemon and flushes window state; escalating to SIGKILL is precisely what
# strands a daemon holding its port. We give the group time to drain and report
# what survived instead of escalating — on a shared dev box, a stray survivor is
# something a human should look at, not something a script should shoot.
#
# THE PID FILE — how a SCRIPT stops this gracefully
#
# The one thing neither path above covers is an external script that wants to
# stop the stack with a single ordinary SIGTERM. Signalling task's pid does
# nothing (task logs the signal and keeps running), and signalling task's
# process GROUP only works when task happens to lead its own group — which is
# true under a terminal or a real process supervisor, but NOT when task is
# launched from a plain script, where it inherits the launcher's pgid and
# `kill -TERM -<task-pid>` fails outright with "No such process".
#
# So we publish the pid of the one process in the chain that both honours
# SIGTERM and can act on it: this one. `<repo>/.dev-electron.pid` (same
# convention and .gitignore entry as .dev-ports.sh) makes the whole teardown
# scriptable without depending on go-task's signal behaviour at all:
#
#     kill -TERM "$(cat reliant/.dev-electron.pid)"
#
# That reaches our trap directly, the group drains gracefully, this script
# exits, task's command returns, and task exits on its own. The file is removed
# on exit; a reader should still check the pid is alive and is actually this
# script before signalling it, because a SIGKILLed supervisor leaves it behind.

set -uo pipefail

readonly ELECTRON_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../electron" && pwd)"
readonly REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly PID_FILE="${REPO_ROOT}/.dev-electron.pid"
# Electron's own budget is 5s for the backend with a 10s hard cap; give the
# whole chain a little more than that before we report a survivor.
readonly DRAIN_TIMEOUT_SECONDS="${DEV_ELECTRON_DRAIN_TIMEOUT:-12}"
readonly SUPERVISOR_PID=$$

log() { echo "[dev-electron-supervise] $*"; }

# Only the main process removes the file, never the watchdog subshell.
remove_pid_file() { [ "$$" = "$SUPERVISOR_PID" ] && rm -f "$PID_FILE"; }
trap remove_pid_file EXIT

if [ -f "$PID_FILE" ]; then
  stale_pid="$(cat "$PID_FILE" 2>/dev/null || true)"
  if [ -n "$stale_pid" ] && kill -0 "$stale_pid" 2>/dev/null; then
    log "WARNING: ${PID_FILE} already names live pid ${stale_pid} — another"
    log "WARNING: dev-electron may be running in this worktree. Taking the file over."
  fi
fi
echo "$SUPERVISOR_PID" > "$PID_FILE"

# Put the npm chain in its own process group so one signal covers every layer.
set -m
(cd "$ELECTRON_DIR" && exec npm run dev) &
readonly CHILD_PID=$!
set +m

log "supervising npm run dev (pid ${CHILD_PID}, process group ${CHILD_PID}); task pid is ${PPID}"
log ""
log "TO STOP THIS STACK GRACEFULLY:"
log "  Ctrl-C                                              (interactive)"
log "  task dev-electron-stop                              (from anywhere)"
log "  kill -TERM \$(cat ${PID_FILE})   (from a script)"
log "Signalling the task pid (${PPID}) does NOT work — go-task catches SIGTERM,"
log "logs it, and keeps running. Signal THIS pid (${SUPERVISOR_PID}) instead."
log ""

# Guards against the trap and the watchdog both firing — a group signal reaches
# us and task, so both paths can race on the same teardown.
teardown_started=""

teardown() {
  local reason="$1"
  [ -n "$teardown_started" ] && return 0
  teardown_started=1

  log "${reason} — SIGTERM to process group ${CHILD_PID}"
  kill -TERM "-${CHILD_PID}" 2>/dev/null || true

  # Wait for Electron's gracefulShutdown() to stop the tools-daemon.
  local waited=0
  while kill -0 "$CHILD_PID" 2>/dev/null; do
    if [ "$waited" -ge "$((DRAIN_TIMEOUT_SECONDS * 4))" ]; then
      log "WARNING: process group ${CHILD_PID} still alive after ${DRAIN_TIMEOUT_SECONDS}s."
      log "WARNING: not escalating to SIGKILL — that is what orphans the tools-daemon."
      log "WARNING: inspect with:  ps -o pid,ppid,pgid,command -g ${CHILD_PID}"
      return 0
    fi
    sleep 0.25
    waited=$((waited + 1))
  done
  log "process group ${CHILD_PID} exited"
}

trap 'teardown "received SIGTERM"' TERM
trap 'teardown "received SIGINT"'  INT
trap 'teardown "received SIGHUP"'  HUP

# The watchdog: task cannot forward SIGKILL, so notice its death directly.
(
  # Drop the inherited handlers: this subshell must never run teardown's
  # bookkeeping or delete the pid file out from under the main process.
  trap - TERM INT HUP EXIT
  while kill -0 "$PPID" 2>/dev/null; do sleep 1; done
  echo "[dev-electron-supervise] task pid ${PPID} vanished (SIGKILL or forced shutdown) — SIGTERM to process group ${CHILD_PID}"
  kill -TERM "-${CHILD_PID}" 2>/dev/null || true
) &
readonly WATCHDOG_PID=$!

# `wait` returns early when a trap fires, so loop until the child is really gone.
while kill -0 "$CHILD_PID" 2>/dev/null; do
  wait "$CHILD_PID"
  child_status=$?
done

kill "$WATCHDOG_PID" 2>/dev/null || true
exit "${child_status:-0}"
