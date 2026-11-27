#!/usr/bin/env bash
# The full Linux suite, as one command: what the Reliant merge queue's gate
# runs on a batch candidate (.reliant/pipeline.yaml), and what main's push CI
# runs on Linux today, on one machine.
#
#   scripts/gate.sh [test]   the test stage (`task gate`): every lane below,
#                            concurrently, one table
#   scripts/gate.sh lint     the lint stage (`task gate:lint`): main's Lint job,
#                            then forge's own lint of this checkout
#
# THE TEST STAGE. Each lane is the command main's CI runs, with its flags:
#
#   unit       go test -race -count=1 over scripts/ci-test-packages.sh unit, full
#              mode (ci.yml `test`; the KCL render tests of internal/templates and
#              internal/codegen run here, in full mode, so scripts/ci-render-tests.sh,
#              the merge tier's subset of them, would only run them twice)
#   cli        go test -race -count=1 over internal/cli (ci.yml `test-cli`)
#   guards     go test -p 1 -count=1 over the guards, no -race (ci.yml
#              `test-short-warm`'s guards step; it says why not -race)
#   e2e        the whole -tags e2e corpus, TestE2E* (task test:e2e, e2e-suite.yml
#              and nightly.yml's select: all)
#   scaffold   forge project new and its own gates (scripts/ci-e2e-scaffold.sh,
#              e2e-scaffold.yml)
#
# The Windows tier is not here: a Linux machine cannot run it. ci.yml's
# `windows` job runs on a push to reliant-queue/** for the same candidate, and
# pipeline.yaml's gate waits for its check (`checks: [Windows]`).
#
# CI is set for every lane, as on a runner: a test that needs docker, a tool or
# an image fails rather than skipping green (testdocker.Unavailable,
# requireTool, requireDockerDaemon). The one exception is a missing docker in a
# Reliant gate (RELIANT_GATE=1): a gate has no docker engine, so those tests
# skip as "skip: docker unavailable (gate)" and the gate counts each skip in
# its verdict (RELIANT_GATE_DOCKER_SKIPS). Host-looking test output alone cannot
# establish that the lane failed ONLY on the host (see below).
#
# EXIT. 0 every lane passed; 1 a lane failed; 2 no lane failed but one could
# not prove anything (UNPROVEN: scaffold's explicit missing-tool exit 2).
# Test output can mix host trouble with real assertion failures, so a failed
# command is conservatively FAIL regardless of host-looking text in its log.
# Only the scaffold preflight's explicit exit code proves a host-only failure.
#
#   GATE_LANES=unit,cli   run only those lanes (a lane left out is listed as
#                         NOT RUN, and the stage cannot pass: exit 2)
#   GATE_JOBS=N           lanes at once (default: all of them)
#   GATE_LOG_DIR=dir      the lanes' logs (default: a fresh temp dir, printed)
#
# Run it as one heavy command (`reliant forge slot run -- task gate`, or your
# harness's heavy flag); it takes 25-60 minutes. Never detach it.
set -uo pipefail

cd "$(git rev-parse --show-toplevel)" || exit 2

mode=${1:-test}

# Even a caller-supplied log directory may be reused (or shared by concurrent
# candidates). Receipts must belong only to this invocation.
log_root=${GATE_LOG_DIR:-${TMPDIR:-/tmp}}
mkdir -p "$log_root" || exit 2
logs=$(mktemp -d "$log_root/forge-gate.XXXXXX") || exit 2
started=$SECONDS

# pkgs <set>: scripts/ci-test-packages.sh's set, refusing an empty one (a lane
# with no packages would pass having run nothing).
pkgs() {
  local out
  out=$(scripts/ci-test-packages.sh "$1") || return 1
  [[ -n "$out" ]] || { echo "scripts/ci-test-packages.sh $1 is empty" >&2; return 1; }
  printf '%s\n' "$out"
}
unit() { mapfile -t p < <(pkgs unit) && ((${#p[@]})) && go test -race -count=1 -timeout 30m "${p[@]}"; }
cli() { mapfile -t p < <(pkgs cli) && ((${#p[@]})) && go test -race -count=1 -timeout 45m "${p[@]}"; }
guards() { mapfile -t p < <(pkgs guards) && ((${#p[@]})) && go test -p 1 -count=1 -timeout 30m "${p[@]}"; }
e2e() { go test -tags e2e -count=1 -timeout 60m -run '^TestE2E' ./internal/cli/; }
scaffold() { scripts/ci-e2e-scaffold.sh; }

# lane NAME: run it with its output in $logs/NAME.log; record "<status> <seconds>".
lane() {
  local name=$1 t0=$SECONDS rc=0 status
  echo "▶ $name"
  (export CI=1 FORGE_E2E_REQUIRE_TOOLS=1 && "$name") >"$logs/$name.log" 2>&1 || rc=$?
  if ((rc == 0)); then
    status=PASS
  elif [[ $name == scaffold && $rc == 2 ]] &&
    [[ $(wc -l <"$logs/$name.log") == 1 ]] &&
    grep -q '^required tool(s) not on PATH: .* (E2E Scaffold needs go, node, pnpm and buf)$' "$logs/$name.log"; then
    status=UNPROVEN
  else
    status=FAIL
  fi
  echo "$status $((SECONDS - t0)) $rc" >"$logs/$name.status"
  echo "■ $name: $status ($((SECONDS - t0))s)"
}

# report LANE...: the table, the tails of what did not pass, and the exit code.
report() {
  local failed=0 unproven=0 s status secs rc
  printf '\n%-10s %-9s %7s  %s\n' LANE RESULT SECONDS LOG
  for s in "$@"; do
    read -r status secs rc 2>/dev/null <"$logs/$s.status" || { status="NOT RUN" secs=0; }
    printf '%-10s %-9s %7s  %s\n' "$s" "$status" "$secs" "$logs/$s.log"
    case $status in
      PASS) ;;
      FAIL) failed=1 ;;
      *) unproven=1 ;;
    esac
  done
  echo
  echo "wall: $((SECONDS - started))s"
  for s in "$@"; do
    read -r status _ 2>/dev/null <"$logs/$s.status" || continue
    [[ $status == PASS ]] && continue
    echo
    echo "── $s ($status): last 60 lines of $logs/$s.log"
    tail -n 60 "$logs/$s.log"
  done
  echo
  if ((failed)); then
    echo "forge gate ($mode): FAILED"
    return 1
  fi
  if ((unproven)); then
    echo "forge gate ($mode): UNPROVEN (a lane could not run, or failed only on the host: no PR is to blame)"
    return 2
  fi
  echo "forge gate ($mode): passed"
}

case $mode in
  test)
    # Not -trimpath, which Taskfile.yml's env block (or `go env`) adds to
    # GOFLAGS: CI's test jobs run without it, because under it a forge test
    # binary cannot show it was built from this checkout and
    # internal/tierguard's renders fail (ci.yml, "NOT -trimpath"). The last
    # -trimpath flag wins, so this one turns any earlier one off.
    export GOFLAGS="${GOFLAGS:-$(go env GOFLAGS)} -trimpath=false"
    all=(e2e cli unit guards scaffold) # longest first
    selected=("${all[@]}")
    if [[ -n "${GATE_LANES:-}" ]]; then
      IFS=',' read -ra selected <<<"$GATE_LANES"
      for s in "${selected[@]}"; do
        [[ " ${all[*]} " == *" $s "* ]] || { echo "error: unknown lane '$s' (${all[*]})" >&2; exit 2; }
      done
    fi
    at_once=${GATE_JOBS:-${#selected[@]}}
    [[ "$at_once" =~ ^[1-9][0-9]*$ ]] || { echo "error: GATE_JOBS must be a positive number" >&2; exit 2; }
    echo "forge gate (test): $(git rev-parse --short HEAD), ${#selected[@]} lane(s), ${at_once} at once, logs in $logs"
    for s in "${selected[@]}"; do
      while (($(jobs -rp | wc -l) >= at_once)); do wait -n; done
      lane "$s" &
    done
    wait
    report "${all[@]}"
    ;;
  lint)
    # main's Lint job (ci.yml `lint`): golangci-lint over the root and pkg/ at
    # the release CI pins, go vet, go vet -tags e2e; then forge's own lint
    # (`forge lint`, the forge built from this checkout: its golangci-lint
    # lanes with their build-tag passes, the contract linter and the rest).
    # forge lint runs after golangci-lint in one lane, so the second
    # golangci-lint pass starts from the first one's cache instead of
    # contending for it. A golangci-lint of another release finds other
    # things, so it cannot judge: that is UNPROVEN (2).
    echo "forge gate (lint): $(git rev-parse --short HEAD), logs in $logs"
    pin=$(sed -n 's|^ *golangci-lint) set -- golangci/golangci-lint v\([^ ]*\) .*|\1|p' .github/actions/install-release-tool/action.yml)
    if ! command -v golangci-lint >/dev/null 2>&1 || ! golangci-lint version 2>/dev/null | grep -qE "version v?${pin//./\\.} "; then
      echo "forge gate (lint): UNPROVEN: golangci-lint v${pin} (the release CI pins) is not on PATH: forge tools install golangci-lint"
      exit 2
    fi
    golangci() {
      golangci-lint run && (cd pkg && golangci-lint run) || return 1
      # Built as `task build` builds it (DevForgeRoot names this checkout).
      go build -trimpath -buildvcs=true -ldflags "-X github.com/reliant-labs/forge/internal/buildinfo.DevForgeRoot=$PWD" -o "$logs/forge" ./cmd/forge &&
        "$logs/forge" lint --quiet --no-fix
    }
    vet() { go vet ./...; }
    vet_e2e() {
      mapfile -t p < <(git grep -lE '^//go:build.*\be2e\b' -- '*.go' ':(exclude,glob)**/testdata/**' | xargs -n1 dirname | sort -u | sed 's|^|./|')
      ((${#p[@]})) || { echo "no file carries the e2e build tag: this check would inspect nothing"; return 1; }
      go vet -tags e2e "${p[@]}"
    }
    lane golangci &
    lane vet &
    lane vet_e2e &
    wait
    report golangci vet vet_e2e
    ;;
  *)
    echo "usage: $0 [test|lint]" >&2
    exit 2
    ;;
esac
