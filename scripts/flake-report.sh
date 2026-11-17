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
# every problem is reported on stdout and the script exits 0. It reruns at most
# MAX_TESTS distinct tests (default 15); a run with more failing tests than that
# is a broad break, not a flake, and is left alone.
#
# The failing tests are read from `gh run view --log-failed`, from any
# `*gate-logs*` artifact the run uploaded (the gate jobs keep their full test
# logs there) and from the `test-short-*` artifacts (scripts/ci-test-shard.sh's
# `go test -json` stream). Attribution never depends on output order, because
# ci-test-shard.sh prints every package summary BEFORE the failed tests' output:
#   1. a `go test -json` stream names each failure's package and test outright;
#   2. the `FAIL <pkg> <Test>` annotation ci-test-shard.sh prints after it
#      (`::error::` / `##[error]`), and the `(package <pkg>)` suffix the merge
#      gate prints, name both;
#   3. otherwise a `--- FAIL: <Test>` is attributed to the `FAIL <pkg>` line
#      that follows it IN THE SAME STEP (a log file, or one job's step in
#      --log-failed); one still unattributed when its step ends is dropped,
#      never carried into a later step's unrelated package.
# Its build tags are taken from the `//go:build` line of the file declaring it.
#
# RACE. Every retry runs under -race: main's Test and Test (internal/cli) jobs
# and its flake gate run under it, and a race-only failure retried without it
# would "pass" and be filed as a flake. A failure whose output reports a data
# race is a real failure and is never retried or filed.
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
# false, and a quiet issue was never closed. Drop CRs at the source, as
# scripts/ci-main-red.sh does.
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
server="${GITHUB_SERVER_URL:-https://github.com}"
label="flake:suspected"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

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

mkdir -p "$work/art"
art_dir="$work/art"
if [[ -n "$logs_dir" ]]; then
  art_dir="$logs_dir"
  # A local gate run: its step logs are the whole record.
  [[ -n "$run_url" ]] || run_url="the local gate run in $logs_dir"
  [[ -n "$sha" ]] || sha="$(git rev-parse HEAD 2>/dev/null || true)"
  : >"$work/failed.log"
else
  run_url="$server/$repo/actions/runs/$run_id"
  if ! gh run view "$run_id" -R "$repo" --log-failed >"$work/failed.raw" 2>"$work/failed.err"; then
    say "could not read the failed logs of $run_url: $(head -c 300 "$work/failed.err")"
    exit 0
  fi
  # Only jobs this Linux runner can reproduce: a Windows or macOS failure rerun
  # here would "pass" and be misreported as a flake.
  awk -F'\t' 'tolower($1) !~ /windows|macos|darwin/' "$work/failed.raw" >"$work/failed.log"
  sha="$(gh run view "$run_id" -R "$repo" --json headSha -q .headSha 2>/dev/null || true)"
  for name in $(gh api "repos/$repo/actions/runs/$run_id/artifacts" -q '.artifacts[] | select(.expired == false) | .name' 2>/dev/null | grep -E 'gate-logs|^test-short-' || true); do
    gh run download "$run_id" -R "$repo" -n "$name" -D "$work/art/$name" >/dev/null 2>&1 || say "could not download artifact $name"
  done
fi

module="$(go list -m 2>/dev/null || true)"

# records: every log line of the run as "<source>\x1f<line>", ANSI colour
# stripped. A source is one step (the job and step names --log-failed prefixes,
# with its timestamp removed) or one artifact log file: the unit within which a
# `--- FAIL` may be attributed to a later `FAIL <pkg>` line.
records() {
  {
    awk -F'\t' '{ src = $1 "\t" $2; line = $0; sub(/^[^\t]*\t[^\t]*\t[0-9TZ:.-]+ ?/, "", line); print src "\x1f" line }' "$work/failed.log"
    find "$art_dir" -type f -name '*.log' -print0 2>/dev/null | sort -z | while IFS= read -r -d '' f; do
      awk -v src="$f" '{ print src "\x1f" $0 }' "$f"
    done
  } | sed -E 's/\x1b\[[0-9;]*m//g'
}

# json_events: every `go test -json` event the run's artifacts hold, one per line.
json_events() {
  find "$art_dir" -type f -name '*.json' -print0 2>/dev/null | sort -z | while IFS= read -r -d '' f; do
    jq -cR 'fromjson? | select(type == "object" and has("Action"))' "$f" 2>/dev/null
  done
}

# One "<pkg>\t<Test>" line per failing top-level test (see the header for the
# three attribution rules, strongest first).
{
  json_events | jq -r 'select(.Action == "fail" and .Test != null and (.Test | contains("/") | not)) | "\(.Package)\t\(.Test)"'
  records | awk -v mod="$module" '
    function qualify(p) { return (mod != "" && p != mod && index(p, mod "/") != 1 && p !~ /^[^\/]*\./) ? mod "/" p : p }
    BEGIN { FS = "\x1f" }
    {
      src = $1; line = $2
      if (src != cur) { delete pending; cur = src }
    }
    line ~ /^(::error::|##\[error\])FAIL [^ ]+ Test[A-Za-z0-9_]+$/ {
      split(line, w, " ")
      print qualify(w[2]) "\t" w[3]
      delete pending[w[3]]
      next
    }
    line ~ /^--- FAIL: Test[A-Za-z0-9_]+/ {
      split(line, w, " ")
      if (match(line, /\(package [^)]+\)/)) {
        print qualify(substr(line, RSTART + 9, RLENGTH - 10)) "\t" w[3]
      } else {
        pending[w[3]] = 1
      }
      next
    }
    line ~ /^FAIL\t[^ \t]+/ {
      split(line, f, "\t")
      for (n in pending) print f[2] "\t" n
      delete pending
      next
    }
    line ~ /^ok  \t/ { delete pending }
  '
} | sort -u >"$work/failing.tsv"

count="$(wc -l <"$work/failing.tsv" | tr -d ' ')"
if [[ "$count" -eq 0 ]]; then
  say "no failing Go test in $run_url (a build, lint or non-Go failure): nothing to rerun"
  exit 0
fi
if [[ "$count" -gt "$max_tests" ]]; then
  say "$count failing tests in $run_url (> $max_tests): a broad break, not a flake; not rerunning"
  exit 0
fi
say "$count failing test(s) in $run_url:"
sed 's/^/  /' "$work/failing.tsv"
[[ "$list_only" -eq 1 ]] && exit 0

if [[ -z "$module" ]]; then
  say "not inside the Go module (go list -m failed): cannot rerun"
  exit 0
fi

# block <Test>: everything the run printed for its failure, from the logs'
# `--- FAIL: <Test>` block and the JSON streams' output for it.
block() {
  records | awk -v t="$1" '
    BEGIN { FS = "\x1f" }
    { line = $2 }
    # From its `=== RUN` (go test prints a race report and the "race detected"
    # line BEFORE `--- FAIL`), through `--- FAIL`, to the next test or summary.
    line == "=== RUN   " t { buf = ""; collecting = 1 }
    collecting { buf = buf line "\n" }
    index(line, "--- FAIL: " t " ") == 1 || line == "--- FAIL: " t {
      printf "%s", (collecting ? buf : line "\n"); collecting = 0; on = 1; next
    }
    collecting && line ~ /^(--- (PASS|SKIP): |ok  |PASS$)/ { collecting = 0 }
    on && line ~ /^(--- |=== |FAIL|ok  |PASS)/ { on = 0 }
    on { print line }
  '
  json_events | jq -j --arg t "$1" 'select(.Action == "output" and .Test == $t) | .Output'
}

# excerpt <Test>: the first failure lines, for the issue body.
excerpt() { block "$1" | head -n 25 | cut -c1-300; }

# raced <Test>: the failure reported a data race. A race is a real defect; a
# retry that happens not to schedule it is no evidence of a flake.
raced() { block "$1" | grep -q -E 'race detected during execution of test|WARNING: DATA RACE'; }

passed=()
cut -f1 "$work/failing.tsv" | sort -u >"$work/pkgs"
while read -r pkg; do
  rel="${pkg#"$module"}"
  if [[ "$rel" == "$pkg" && "$pkg" != "$module" ]]; then
    say "skip $pkg: not in module $module"
    continue
  fi
  dir=".${rel}"
  [[ -d "$dir" ]] || { say "skip $pkg: no directory $dir at this commit"; continue; }
  mapfile -t all < <(awk -F'\t' -v p="$pkg" '$1 == p { print $2 }' "$work/failing.tsv")
  tests=()
  for t in "${all[@]}"; do
    if raced "$t"; then
      say "  $t reported a data race: a real failure, not retried or reported as a flake"
    else
      tests+=("$t")
    fi
  done
  [[ "${#tests[@]}" -gt 0 ]] || continue
  tags=""
  for t in "${tests[@]}"; do
    file="$(grep -l -E "^func ${t}\(" "$dir"/*_test.go 2>/dev/null | head -1)"
    [[ -n "$file" ]] || continue
    tag="$(sed -n -E '1,5s#^//go:build ([A-Za-z0-9_]+)$#\1#p' "$file" | head -1)"
    [[ -n "$tag" && " $tags " != *" $tag "* ]] && tags="${tags:+$tags,}$tag"
  done
  regex="^($(IFS='|'; echo "${tests[*]}"))\$"
  say "rerunning ${tests[*]} in $pkg${tags:+ (tags $tags)}"
  log="$work/rerun-$(echo "$pkg" | tr '/.' '__').log"
  # -race always: a race-only failure must not "pass" a retry without it.
  go test -race -count=1 -v -timeout 20m ${tags:+-tags "$tags"} -run "$regex" "$dir" >"$log" 2>&1
  for t in "${tests[@]}"; do
    if grep -q -E "^--- PASS: ${t} " "$log" && ! grep -q -E "^--- FAIL: ${t} " "$log"; then
      say "  $t PASSED on retry: suspected flake"
      passed+=("$pkg	$t")
    elif grep -q -E "^--- SKIP: ${t} " "$log"; then
      say "  $t skipped on retry (its environment is missing here): inconclusive"
    else
      say "  $t failed again (or did not run): a real failure, not reported as a flake"
    fi
  done
done <"$work/pkgs"

if [[ "${#passed[@]}" -eq 0 ]]; then
  say "no test passed on retry"
  exit 0
fi
if [[ "$dry_run" -eq 1 ]]; then
  say "--dry-run: would file ${#passed[@]} flake:suspected issue(s)"
  exit 0
fi

gh label create "$label" -R "$repo" --color FBCA04 \
  --description "A test failed on main, then passed on a retry of the same commit" >/dev/null 2>&1 || true

for entry in "${passed[@]}"; do
  pkg="${entry%%	*}"
  t="${entry#*	}"
  short="${pkg#"$module"/}"
  title="flake:suspected: $t ($short)"
  # The newest issue for this test in any state: a closed one (fixed, or swept
  # as stale) is reopened, never duplicated.
  existing="$(gh issue list -R "$repo" --label "$label" --state all --search "\"$t\" in:title" \
    --json number,title,state -q ".[] | select(.title == \"$title\") | \"\(.number) \(.state)\"" 2>/dev/null | head -1)"
  state="${existing#* }" existing="${existing%% *}"
  ex="$(excerpt "$t")"
  if [[ -n "$existing" ]]; then
    if [[ "$state" == CLOSED ]]; then
      if gh issue reopen "$existing" -R "$repo" >/dev/null; then
        say "reopened #$existing ($t)"
      else
        say "could not reopen #$existing"
      fi
    fi
    # shellcheck disable=SC2016 # the backticks are markdown, not command substitution
    if gh issue comment "$existing" -R "$repo" --body "$(printf 'Failed then passed on retry again: %s (commit `%s`).\n\n```\n%s\n```\n' "$run_url" "${sha:0:12}" "$ex")" >/dev/null; then
      say "commented on #$existing ($t)"
    else
      say "could not comment on #$existing"
    fi
    continue
  fi
  # shellcheck disable=SC2016 # the backticks are markdown, not command substitution
  body="$(printf '`%s` in `%s` failed on main in %s (commit `%s`), then **passed** when rerun alone on the same commit (`go test -race -count=1 -run ^%s$`).\n\n### Failure\n\n```\n%s\n```\n\n### What to do\n\nOwner policy: flaky = delete immediately; rewrite as a new deterministic test if it is worth having. Delete the test (or its flaky assertion) and open a `flake:removed` issue with its body and the failure signature. If the failure is the host (inotify, fd or disk budget, a download), make the test skip with a message naming the missing resource.\n\nFiled by `scripts/flake-report.sh`; later occurrences are added as comments.\n' \
    "$t" "$pkg" "$run_url" "${sha:0:12}" "$t" "$ex")"
  if gh issue create -R "$repo" --title "$title" --label "$label" --body "$body" >/dev/null; then
    say "opened an issue for $t"
  else
    say "could not open an issue for $t"
  fi
done
exit 0
