#!/usr/bin/env bash
# gate.sh — the Reliant merge queue's gate for reliant (`task gate`): today's
# main push suite (.github/workflows/ci.yml on a push to main) as one command,
# on one machine. .reliant/pipeline.yaml names it as the gate's test stage.
#
#   scripts/gate.sh             every lane below, concurrently, one table
#
# THE LANES, each what main's CI runs on a push:
#
#   static   release gate part static: npm, generate, docs, build, webbuild
#            (the web bundle: lint, production build, verify-bundle), demo-size,
#            typecheck, vet  (ci.yml's generate, webbuild and compile parts)
#   lint     release gate part lint: unscoped forge lint (golangci-lint over the
#            whole module, buf lint, every whole-project lane, the frontends)
#   go       release gate part go: task test over every package, -race
#            -count=1, no -short (ci.yml's go 1/4..4/4: one machine runs the
#            packages in parallel itself, so the shards would only add checkouts)
#   web      release gate part web: test-web, test-electron, integration, e2e
#   ui       Playwright (task test:web:e2e: desktop Chromium and the touch
#            Chromium/WebKit projects, ci.yml's playwright matrix), then the
#            electron package smoke (task build:electron:smoke). One lane: both
#            build frontends/web/dist in this checkout.
#   repo     task ci:migration-order and task ci:repo-checks against GATE_BASE
#
# Each release gate part runs scripts/release-gate.sh run --part in a private
# checkout of HEAD of its own (RELEASE_GATE_DIR under the log dir), never
# recorded as a release and never published (RELEASE_GATE_PUBLISH=0), and
# --force: the verdict is this run's, not a record from elsewhere. The other
# lanes run in this checkout, after one install (task deps:web deps:electron).
#
# Left out, with the reason: the vendored control-plane contract check
# (cp-contract) reads the private control-plane repository, which a gate
# without credentials cannot; it stays a PR-time check. The release-gate record
# and the test upload are bookkeeping, not tests.
#
# EXIT. 0 every lane passed; 1 a lane failed; 2 no lane failed but one proved
# nothing: it could not start (exit 2 from release-gate.sh: no private
# checkout, another run's lock), or it failed only on the host (HOST_RE in its
# logs: no docker daemon, the disk, a registry rate limit, a browser's missing
# system packages). The queue never lands on 2 and never blames a PR for it.
#
#   GATE_BASE=<rev>     the queue's base (main before the batch), for the repo
#                       lane's ranges (default: CI_BASE, else the merge base
#                       with origin/main). The Reliant queue's gate sets both
#                       GATE_BASE and CI_BASE and fetches the base, and its
#                       checkout has no origin/main.
#   GATE_LANES=go,web   run only those lanes (the rest are NOT RUN: exit 2)
#   GATE_JOBS=N         lanes at once (default: all)
#   GATE_LOG_DIR=dir    logs and the private checkouts (default: a fresh temp dir)
#   GATE_KEEP=1         keep the private checkouts; by default each is removed
#                       (git worktree remove) when the gate ends, logs kept
#
# 40-70 minutes on a loaded host; run it as one heavy command in the
# background, never detached.
set -uo pipefail

cd "$(git rev-parse --show-toplevel)" || exit 2
root=$PWD
HOST_RE='Cannot connect to the Docker daemon|no space left on device|ENOSPC|toomanyrequests|429 Too Many Requests|Host system is missing dependencies|cannot allocate memory|too many open files'

logs=${GATE_LOG_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/reliant-gate.XXXXXX")}
mkdir -p "$logs"
started=$SECONDS
sha=$(git rev-parse --verify "HEAD^{commit}") || exit 2
# The release gate parts' private checkouts are git worktrees of this clone,
# with node_modules each: removed when the gate ends, so a run on a shared
# machine leaves neither a worktree registration nor the disk behind.
cleanup() {
  [[ "${GATE_KEEP:-0}" == 1 ]] && return 0
  local snap
  for snap in "$logs"/checkout-*/snap; do
    [[ -e "$snap" ]] || continue
    git worktree remove --force "$snap" >/dev/null 2>&1 || rm -rf "$snap"
  done
  git worktree prune >/dev/null 2>&1 || true
}
trap cleanup EXIT

if [[ -n "$(git status --porcelain --untracked-files=no)" ]]; then
  echo "warning: the tree has uncommitted changes; the release gate parts judge HEAD ($(git rev-parse --short HEAD)) as committed" >&2
fi

# part NAME: release-gate.sh's part NAME on HEAD, in a private checkout of its own.
part() {
  RELEASE_GATE_PUBLISH=0 RELEASE_GATE_DIR="$logs/checkout-$1" "$root/scripts/release-gate.sh" run --force --part "$1" "$sha"
}
lane_static() { part static; }
lane_lint() { part lint; }
lane_go() { part go; }
lane_web() { part web; }
lane_ui() {
  task deps:web deps:electron || return 1
  task test:web:e2e || return 1
  task build:electron:smoke
}
lane_repo() {
  local base=${GATE_BASE:-${CI_BASE:-}}
  [[ -n "$base" ]] || base=$(git merge-base HEAD origin/main 2>/dev/null) ||
    { echo "repo: no base: set GATE_BASE (the queue's base), or fetch origin/main"; return 2; }
  task ci:migration-order BASE="$base" && task ci:repo-checks BASE="$base"
}

# part_logs NAME: the logs a release gate part's output names, resolved where
# they live: as written, or relative to the part's private checkout
# ($logs/checkout-NAME, where release-gate.sh runs), or anywhere under it by
# name; and every *.log under that checkout, since a passing step prints no
# path. Printed to stdout for HOST_RE.
part_logs() {
  local name=$1 dir="$logs/checkout-$1" f
  {
    grep -oE '[^ ;()]+\.log' "$logs/$name.log" | sort -u | while read -r f; do
      if [[ -f "$f" ]]; then cat "$f"
      elif [[ -f "$dir/$f" ]]; then cat "$dir/$f"
      elif [[ -d "$dir" ]]; then find "$dir" -name "$(basename "$f")" -type f -exec cat {} + 2>/dev/null
      fi
    done
    [[ -d "$dir" ]] && find "$dir" -path "$dir/snap" -prune -o -name '*.log' -type f -exec cat {} + 2>/dev/null
  } 2>/dev/null || true
}

# lane NAME: run it with its output in $logs/NAME.log; record "<status> <seconds>".
# A release gate part keeps each step's output in its own log, which the
# summary names: those count for HOST_RE too.
lane() {
  local name=$1 t0=$SECONDS rc=0 status
  echo "▶ $name"
  ("lane_$name") >"$logs/$name.log" 2>&1 || rc=$?
  if ((rc == 0)); then
    status=PASS
  elif ((rc == 2)) || grep -qE "$HOST_RE" "$logs/$name.log" || part_logs "$name" | grep -qE "$HOST_RE"; then
    status=UNPROVEN
  else
    status=FAIL
  fi
  echo "$status $((SECONDS - t0)) $rc" >"$logs/$name.status"
  echo "■ $name: $status ($((SECONDS - t0))s)"
}

all=(go web static lint ui repo) # longest first
selected=("${all[@]}")
if [[ -n "${GATE_LANES:-}" ]]; then
  IFS=',' read -ra selected <<<"$GATE_LANES"
  for s in "${selected[@]}"; do
    [[ " ${all[*]} " == *" $s "* ]] || { echo "error: unknown lane '$s' (${all[*]})" >&2; exit 2; }
  done
fi
at_once=${GATE_JOBS:-${#selected[@]}}
[[ "$at_once" =~ ^[1-9][0-9]*$ ]] || { echo "error: GATE_JOBS must be a positive number" >&2; exit 2; }
echo "reliant gate: $(git rev-parse --short HEAD), ${#selected[@]} lane(s), ${at_once} at once, logs in $logs"
declare -A pid_of=()
for s in "${selected[@]}"; do
  while (($(jobs -rp | wc -l) >= at_once)); do wait -n; done
  lane "$s" &
  pid_of[$s]=$!
done
# Join every lane by its pid, so the summary below only ever reads a lane
# that has finished (review B1 on #3823). A lane whose process ended without
# writing its status (killed, a bug in this script) proved nothing: UNPROVEN,
# with its exit code, never a silent PASS.
for s in "${selected[@]}"; do
  rc=0
  wait "${pid_of[$s]}" || rc=$?
  [[ -s "$logs/$s.status" ]] || echo "UNPROVEN 0 $rc" >"$logs/$s.status"
done

failed=0 unproven=0
printf '\n%-8s %-9s %7s  %s\n' LANE RESULT SECONDS LOG
for s in "${all[@]}"; do
  read -r status secs _ 2>/dev/null <"$logs/$s.status" || { status="NOT RUN" secs=0; }
  printf '%-8s %-9s %7s  %s\n' "$s" "$status" "$secs" "$logs/$s.log"
  case $status in
    PASS) ;;
    FAIL) failed=1 ;;
    *) unproven=1 ;;
  esac
done
echo
echo "wall: $((SECONDS - started))s"
for s in "${all[@]}"; do
  read -r status _ 2>/dev/null <"$logs/$s.status" || continue
  [[ $status == PASS ]] && continue
  echo
  echo "── $s ($status): last 60 lines of $logs/$s.log"
  tail -n 60 "$logs/$s.log"
done
echo
if ((failed)); then
  echo "reliant gate: FAILED"
  exit 1
fi
if ((unproven)); then
  echo "reliant gate: UNPROVEN (a lane could not run, or failed only on the host: no PR is to blame)"
  exit 2
fi
echo "reliant gate: passed"
