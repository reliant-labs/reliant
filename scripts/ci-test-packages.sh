#!/usr/bin/env bash
# Partition the module's packages between ci.yml's three Go test jobs.
#
#   scripts/ci-test-packages.sh unit         every package EXCEPT internal/cli and the guards
#   scripts/ci-test-packages.sh cli          exactly internal/cli
#   scripts/ci-test-packages.sh guards       exactly the guards
#   scripts/ci-test-packages.sh unit cli     the union of the named sets (Windows runs this)
#   scripts/ci-test-packages.sh check        prove the three sets partition `go list ./...`
#
#   scripts/ci-test-packages.sh short I N    the pull-request tier's package shard I of N (0-based)
#
# Taskfile.yml's `task test` runs the same three sets with the same flags, so
# the local full tier and CI cannot disagree about what runs where.
#
# THE PULL-REQUEST TIER IS PARTITIONED DIFFERENTLY. A pull request runs the
# -short tier without -race (ci.yml's `test-short` matrix, driven by
# scripts/ci-test-shard.sh); the three sets above, under -race and in full
# mode, run on every push to main. Under -short the guards skip their whole-
# repo work and take seconds, and internal/cli's ~3,200 tests run in ~50s on a
# CI runner, so the PR tier needs neither set apart: it is every package, dealt
# round-robin in `go list` order onto `short I N`. What a shard spends is
# compile and link time, which a package boundary splits as well as anything
# finer would. internal/ciguard proves ci.yml's matrix names every index of
# its shard count, so no shard can drop out of the matrix with its packages.
#
# WHY THE GUARDS HAVE THEIR OWN JOB. internal/tierguard renders four whole forge
# projects, internal/removalguard regex-scans every file in the repository, and
# internal/deadcodeguard type-checks the whole program — every dependency
# included — once per target OS. They are the slowest and the largest packages
# in the suite. Run inside `go test -race ./...` they co-resided with a hundred
# other test binaries on one 16 GB runner, and the Test job was OOM-killed ("The
# runner has received a shutdown signal", exit 143): roughly one run in three
# for tierguard and removalguard until #304 moved them here, and most runs for
# deadcodeguard once its scan grew to three OS loads (#469; ~23 GB RSS under
# -race). In their own job their peak never meets the rest of the suite's —
# bounded by the job boundary, not by the luck of go test's package scheduling.
#
# WHY internal/cli HAS ITS OWN JOB. It is the largest test binary in the
# module, and the most-changed package. Under -race it alone took 665-692s of
# the Test job's 1215-1253s `go test` step, sharing the runner's four cores
# with every other package's compile and run. Split, each half gets a runner
# to itself and the two run side by side, so the unit lane's wall time is the
# slower half instead of both halves contending for one machine. Only the
# package itself moves: its subpackages (internal/cli/lint,
# internal/cli/scaffold, ...) are ordinary-sized and stay in `unit`.
#
# WHY THIS SCRIPT. A package list typed into several workflow steps can drift:
# add a guard to one list and forget another, and it runs twice or — worse —
# not at all while every job stays green. So the lists live HERE, once, and
# `check` fails the job unless unit ∪ cli ∪ guards == `go list ./...` and the
# three are pairwise disjoint. A build tag was rejected for the same reason
# e2e-suite.yml documents: tagged tests are invisible to golangci-lint and to
# every plain `go test ./...`, which is how 47 e2e tests once went unexecuted
# while CI stayed green. The guards stay ordinary tests; locally
# `go test ./...` still runs them.
set -euo pipefail

# The guard packages. Import paths, one per line.
guards=(
  github.com/reliant-labs/forge/internal/deadcodeguard
  github.com/reliant-labs/forge/internal/removalguard
  github.com/reliant-labs/forge/internal/tierguard
  github.com/reliant-labs/forge/internal/writeguard
)

# The package that gets a job of its own.
cli=(
  github.com/reliant-labs/forge/internal/cli
)

# `go list ./...` once per invocation: it is the slow part, and `check` needs
# it four times over.
all_cache=""
all() {
  [[ -n "$all_cache" ]] || all_cache=$(go list ./...)
  printf '%s\n' "$all_cache"
}
guard_set() { printf '%s\n' "${guards[@]}"; }
cli_set() { printf '%s\n' "${cli[@]}"; }
unit_set() { all | grep -vxF -f <(guard_set) -f <(cli_set); }

# short_set I N: every package, dealt round-robin.
short_set() {
  local i=$1 n=$2
  if ! [[ "$i" =~ ^[0-9]+$ && "$n" =~ ^[1-9][0-9]*$ ]] || ((i >= n)); then
    echo "usage: $0 short I N   (0 <= I < N)" >&2
    exit 2
  fi
  all | awk -v i="$i" -v n="$n" '(NR - 1) % n == i'
}

usage() {
  echo "usage: $0 unit|cli|guards [unit|cli|guards ...] | check | short I N" >&2
  exit 2
}

set_of() {
  case "$1" in
    unit) unit_set ;;
    cli) cli_set ;;
    guards) guard_set ;;
    *) usage ;;
  esac
}

case "${1:-}" in
  check)
    # Primed here, in this shell: every $(...) below is a subshell, and an
    # assignment inside one does not survive it.
    all_cache=$(go list ./...)
    all_pkgs=$(all | sort)
    status=0
    named=$(printf '%s\n%s\n' "$(guard_set)" "$(cli_set)" | sort)
    # Every named package must be real: a renamed or deleted one would
    # otherwise leave its job testing nothing and passing.
    missing=$(comm -13 <(printf '%s\n' "$all_pkgs") <(printf '%s\n' "$named"))
    if [[ -n "$missing" ]]; then
      echo "::error::package(s) named in scripts/ci-test-packages.sh are not in \`go list ./...\` — renamed or deleted?"
      printf '  %s\n' $missing
      status=1
    fi
    # Pairwise disjoint: a package in two sets runs in two jobs.
    dup=$(printf '%s\n%s\n%s\n' "$(unit_set)" "$(cli_set)" "$(guard_set)" | sort | uniq -d)
    if [[ -n "$dup" ]]; then
      echo "::error::package(s) in more than one job:"
      printf '  %s\n' $dup
      status=1
    fi
    union=$(printf '%s\n%s\n%s\n' "$(unit_set)" "$(cli_set)" "$(guard_set)" | sort -u)
    if [[ "$union" != "$all_pkgs" ]]; then
      echo "::error::the three jobs do not cover \`go list ./...\` exactly:"
      diff <(printf '%s\n' "$all_pkgs") <(printf '%s\n' "$union") || true
      status=1
    fi
    if [[ $status -eq 0 ]]; then
      n() { printf '%s\n' "$1" | grep -c .; }
      echo "partition ok: $(n "$(unit_set)") unit + $(n "$(cli_set)") cli + $(n "$(guard_set)") guard = $(n "$all_pkgs") packages"
    fi
    exit $status
    ;;
  short)
    [[ $# -eq 3 ]] || usage
    short_set "$2" "$3"
    ;;
  "")
    usage
    ;;
  *)
    for s in "$@"; do set_of "$s"; done
    ;;
esac
