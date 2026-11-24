#!/usr/bin/env bash
# flake-report.sh — rerun the Go tests a failed CI run on main reported, once,
# and file a `flake:suspected` issue for each that passes on the retry.
#
#   scripts/flake-report.sh <run-id> [--dry-run]            a GitHub Actions run
#   scripts/flake-report.sh --logs <dir> [--sha <sha>] [--run-url <url>] [--dry-run]
#                                                           a local gate's log directory
#   --list prints the failing tests it found and stops (no rerun, no issue).
#   scripts/flake-report.sh --sweep [--dry-run]             close the stale ones (below)
#
# Run it from the root of the Go module at the run's commit.
#
# Owner policy (2026-11-01): flaky = delete immediately; rewrite as a new
# deterministic test if it's worth having. A deleted test gets a
# `flake:removed` issue. New tests are kept
# out by the flake check (each added or edited test runs three times before it
# merges). This script is the net for EXISTING tests: it turns "failed, then
# passed on the same commit" into an issue somebody triages, instead of a red
# run somebody re-runs until it is green.
#
# It never fails the workflow that runs it and never changes a run's verdict:
# every problem is reported on stdout and the script exits 0.
#
# The rerun-and-file path is a thin wrapper over `forge test flakes confirm`
# and `forge test flakes --issues` (below): forge dogfoods its own port of
# it. They read the failing tests from `gh run view --log-failed`, the
# `*gate-logs*` and `test-short-*` artifacts (scripts/ci-test-shard.sh's
# `go test -json` stream) or the --logs directory, and attribute each
# failure independently of output order, as this script did: a -json stream
# names package and test outright; the `::error::FAIL <pkg> <Test>`
# annotation and the `(package <pkg>)` suffix name both; otherwise a
# `--- FAIL` belongs to the `FAIL <pkg>` line that follows it in the same
# step, never a later step's. Every retry runs under -race (main's test jobs
# do), and a failure that reports a data race is never retried or filed.
# --sweep is still this script's own: confirm has no equivalent of it.
#
# ONE ISSUE PER TEST, closed when nothing keeps it alive. A test that flakes
# again comments on its issue, reopening it if it was closed, so its history
# stays in one thread. --sweep closes an open issue whose test no longer
# exists in the checkout it runs from (run it from main), or that has not
# flaked again for FLAKE_SWEEP_DAYS days (default 7).
#
# Environment: GH_TOKEN (actions:read, issues:write), GITHUB_REPOSITORY
# (owner/name; defaults to the current repo), MAX_TESTS.

set -uo pipefail

# A native jq.exe (Windows, where internal/ciguard's test runs this script
# under Git Bash) ends every line with CRLF. --sweep reads the issue's age as
# the last field of a jq line, so it carried the CR, `[[ "12\r" -ge 7 ]]` was
# false, and a quiet issue was never closed. Drop CRs at the source.
jq() { command jq "$@" | tr -d '\r'; }

run_id="" logs_dir="" sha="" run_url="" dry_run=0 list_only=0 sweep=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) dry_run=1 ;;
    --list) list_only=1 ;;
    --sweep) sweep=1 ;;
    --logs) logs_dir="${2:-}"; shift ;;
    --sha) sha="${2:-}"; shift ;;
    --run-url) run_url="${2:-}"; shift ;;
    *) run_id="$1" ;;
  esac
  shift
done
if [[ -z "$run_id" && -z "$logs_dir" && "$sweep" -eq 0 ]]; then
  echo "usage: $0 <run-id> [--dry-run] | $0 --logs <dir> [--sha <sha>] [--run-url <url>] [--dry-run] | $0 --sweep [--dry-run]" >&2
  exit 0
fi
max_tests="${MAX_TESTS:-15}"
repo="${GITHUB_REPOSITORY:-$(gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null)}"
label="flake:suspected"

say() { echo "flake-report: $*"; }

# --sweep: close each open flake:suspected issue whose test is gone from this
# checkout, or whose last occurrence (the issue's creation, or the newest
# "Failed then passed on retry again" comment below) is FLAKE_SWEEP_DAYS old.
if [[ "$sweep" -eq 1 ]]; then
  days="${FLAKE_SWEEP_DAYS:-7}"
  now="${FLAKE_SWEEP_NOW:-$(date -u +%s)}"
  module="$(awk '$1 == "module" { print $2; exit }' go.mod 2>/dev/null)"
  if ! issues="$(gh issue list -R "$repo" --label "$label" --state open --limit 500 \
      --json number,title,createdAt,comments 2>&1)"; then
    say "--sweep: could not list the open $label issues: ${issues:0:300}"
    exit 0
  fi
  title_re='^flake:suspected: (Test[A-Za-z0-9_]*) \((.+)\)$'
  while IFS=$'\t' read -r num title last age; do
    [[ -n "$num" ]] || continue
    reason=""
    # Only from a Go module's root: anywhere else every test would look gone.
    if [[ -n "$module" && "$title" =~ $title_re ]]; then
      t="${BASH_REMATCH[1]}" short="${BASH_REMATCH[2]}"
      dir="./$short"
      [[ "$short" == "$module" ]] && dir="."
      if ! grep -q -E "^func ${t}\(" "$dir"/*_test.go 2>/dev/null; then
        # shellcheck disable=SC2016 # the backticks are markdown, not command substitution
        reason="$(printf '`%s` no longer exists in `%s` at `%s`' "$t" "$short" "$(git rev-parse --short=12 HEAD 2>/dev/null)")"
      fi
    fi
    if [[ -z "$reason" && "$age" -ge "$days" ]]; then
      reason="it has not flaked again for $age days (last seen $last)"
    fi
    [[ -n "$reason" ]] || continue
    if [[ "$dry_run" -eq 1 ]]; then
      say "--dry-run: would close #$num: $reason"
      continue
    fi
    if gh issue close "$num" -R "$repo" --reason "not planned" \
        --comment "Closed by \`scripts/flake-report.sh --sweep\`: $reason. A new flake of this test reopens it." >/dev/null; then
      say "closed #$num: $reason"
    else
      say "could not close #$num"
    fi
  done < <(jq -r --argjson now "$now" '.[]
    | ([.createdAt] + [.comments[]? | select(.body | startswith("Failed then passed on retry again")) | .createdAt] | max) as $last
    | [.number, .title, $last, ((($now - ($last | fromdateiso8601)) / 86400) | floor)] | @tsv' <<<"$issues")
  exit 0
fi

# The rerun-and-file path: forge dogfoods its own flake confirm. `forge test
# flakes confirm` (this checkout's forge, scripts/ci-forge.sh) is the port of
# what this script did here: it reads the same sources (--log-failed, the
# gate-logs and test-short-* artifacts, or --logs), attributes failures the
# same three ways, never retries a race, reruns each failing test once,
# alone, under -race at this commit, and records the reruns as lane confirm
# under .forge/tests/runs/. `forge test flakes --issues` then files or
# comments on one flake:suspected issue per confirmed test, under the title
# this script used (`flake:suspected: <Test> (<package>)`), so the issues it
# filed are the ones updated and a closed one is reopened.
#
# What the port changed, on purpose (`forge test flakes confirm --help`):
#   - a failure matching a host signature (network, rate limits, disk, ports,
#     docker, a timeout under load) is host health: not rerun, not filed;
#   - more than MAX_TESTS failures no longer means "a broad break, rerun
#     nothing": 3+ failures sharing a package, harness or signature are one
#     infra event (not rerun), and the first MAX_TESTS of the rest are rerun;
#   - a rerun that could not run (its package failed, it was skipped) is
#     inconclusive, as a skip already was;
#   - --issues also closes the open issue of a test the record saw go quiet;
#     --sweep below still does the daily closing.
# The issues are synced from the local record: in CI that is this run alone;
# run locally with --logs, it is this machine's whole record.
cf=()
if [[ -n "$logs_dir" ]]; then
  cf=(--logs "$logs_dir")
  [[ -n "$sha" ]] && cf+=(--sha "$sha")
  [[ -n "$run_url" ]] && cf+=(--run-url "$run_url")
else
  cf=(--run "$run_id")
fi
[[ -n "$repo" ]] && cf+=(--repo "$repo")
[[ "$list_only" -eq 1 ]] && cf+=(--list)
scripts/ci-forge.sh test flakes confirm "${cf[@]}" --max "$max_tests" || say "forge test flakes confirm exited $?"
[[ "$list_only" -eq 1 ]] && exit 0

issues=(--issues)
[[ -n "$repo" ]] && issues+=(--repo "$repo")
[[ "$dry_run" -eq 1 ]] && issues+=(--dry-run)
scripts/ci-forge.sh test flakes "${issues[@]}" || say "forge test flakes --issues exited $?"
exit 0
