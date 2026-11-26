#!/usr/bin/env bash
# coverage-floor.sh — total Go statement coverage, held to a floor that only
# goes up. The same file in reliant and control-plane (owner, 2026-11-26: a
# floor per repo, no test-writing push; forge has its own 80% target).
#
#   coverage-floor.sh measure [--json] <profile>...
#       Union the Go cover profiles and print the total: a block counts as
#       covered when ANY profile hit it, never the last profile's count (one
#       file per `go test` invocation, unioned here; a block two invocations
#       instrumented counts once). --json prints
#       {"percent","covered","statements","generated"}.
#   coverage-floor.sh check [--json-out <file>] <profile>...
#       measure, then compare with the floor file: exit 0 at or above it, 1
#       below it, 2 when it cannot measure (no profile, an unreadable floor).
#       Prints the number, the floor and how to raise or lower the floor.
#       --json-out also writes measure's JSON plus "floor" and "result".
#   coverage-floor.sh merge <profile>...
#       The union as one profile on stdout (mode: atomic, a block's counts
#       summed), for a caller that carries one file instead of several.
#   coverage-floor.sh raise (--to <percent> | <profile>...) [--headroom <points>]
#                           [--lower --reason "<why>"]
#       Set the floor to the measured number minus the headroom (default 0.5
#       points, for run-to-run noise), truncated to two decimals. Only upward:
#       a lower result changes nothing unless --lower is given, which needs
#       --reason and records it in the floor file (say it in the commit too).
#
#   COVERAGE_FLOOR_FILE   the floor file (default <repo root>/coverage-floor.txt)
#   COVERAGE_FLOOR_NAME   how check names it (default its path in the repo)
#
# EXCLUDED from the denominator, as forge's coverage report counts generated
# code on a line of its own (forge pkg/testreport.IsGenerated): every file in a
# directory named gen or testdata, *_gen.go (mock_gen.go included) and *.pb.go.
# The floor file's header says the same; keep the two in step.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
floor_file=${COVERAGE_FLOOR_FILE:-$root/coverage-floor.txt}
floor_name=${COVERAGE_FLOOR_NAME:-${floor_file#"$root"/}}
die() { printf 'coverage-floor: %s\n' "$*" >&2; exit 2; }

# tally <profile>...: "<covered> <statements> <generated>" over the union.
tally() {
  local p
  [ $# -gt 0 ] || die "no cover profile given"
  for p in "$@"; do [ -r "$p" ] || die "cannot read cover profile $p"; done
  LC_ALL=C awk '
    FNR == 1 && /^mode:/ { next }
    NF == 3 && $1 ~ /:[0-9]+\.[0-9]+,[0-9]+\.[0-9]+$/ {
      stmts[$1] = $2
      if ($3 > 0) hit[$1] = 1
      next
    }
    NF > 0 { printf "coverage-floor: %s:%d: not a cover profile line: %s\n", FILENAME, FNR, $0 > "/dev/stderr"; bad = 1; exit 2 }
    END {
      if (bad) exit 2
      for (k in stmts) {
        f = k; sub(/:[0-9.,]+$/, "", f)
        n = split(f, seg, "/")
        gen = (seg[n] ~ /(_gen\.go|\.pb\.go)$/)
        for (i = 1; i < n; i++) if (seg[i] == "gen" || seg[i] == "testdata") gen = 1
        if (gen) { generated += stmts[k]; continue }
        total += stmts[k]
        if (k in hit) covered += stmts[k]
      }
      printf "%d %d %d\n", covered, total, generated
    }' "$@"
}

# pct <covered> <statements>: the percentage, two decimals.
pct() { awk -v c="$1" -v s="$2" 'BEGIN { printf "%.2f", (s > 0 ? 100 * c / s : 0) }'; }

# read_floor: the floor file's number (its first line that is not a comment).
read_floor() {
  [ -r "$floor_file" ] || die "no floor file $floor_file"
  local f
  f=$(sed -e 's/#.*//' -e 's/[[:space:]]//g' "$floor_file" | grep -v '^$' | head -1)
  [[ "$f" =~ ^[0-9]+(\.[0-9]+)?$ ]] || die "$floor_file holds no floor (a number such as 70.2 on a line of its own)"
  echo "$f"
}

# write_floor <value>: replace the floor file's number, keeping every comment.
write_floor() {
  local tmp="$floor_file.tmp.$$"
  awk -v v="$1" '!done && !/^[[:space:]]*(#|$)/ { print v; done = 1; next } { print }' "$floor_file" >"$tmp"
  mv -f "$tmp" "$floor_file"
}

cmd_measure() {
  local json=0 c s g
  [ "${1:-}" = --json ] && { json=1; shift; }
  read -r c s g < <(tally "$@") || exit 2
  [ "$s" -gt 0 ] || die "the profiles count no statement outside generated code"
  if [ "$json" = 1 ]; then
    printf '{"percent":%s,"covered":%d,"statements":%d,"generated":%d}\n' "$(pct "$c" "$s")" "$c" "$s" "$g"
  else
    printf '%s%% of Go statements (%d of %d; generated code excluded: %d statements)\n' "$(pct "$c" "$s")" "$c" "$s" "$g"
  fi
}

cmd_check() {
  local out="" c s g floor p ok result
  if [ "${1:-}" = --json-out ]; then out=${2:?--json-out needs a file}; shift 2; fi
  read -r c s g < <(tally "$@") || exit 2
  [ "$s" -gt 0 ] || die "the profiles count no statement outside generated code"
  floor=$(read_floor)
  p=$(pct "$c" "$s")
  # Exact, not the rounded percentage: covered/statements >= floor/100.
  ok=$(awk -v c="$c" -v s="$s" -v f="$floor" 'BEGIN { print (100 * c >= f * s) ? 1 : 0 }')
  result=PASS; [ "$ok" = 1 ] || result=FAIL
  printf 'coverage: %s%% of Go statements (%d of %d; generated code excluded: %d statements)\n' "$p" "$c" "$s" "$g"
  printf 'floor:    %s%% (%s)\n' "$floor" "$floor_name"
  if [ "$ok" = 1 ]; then
    printf 'PASS: %s points above the floor. To lock the gain in: task coverage:raise-floor (the floor becomes the latest measured number minus 0.5).\n' \
      "$(awk -v p="$p" -v f="$floor" 'BEGIN { printf "%.2f", p - f }')"
  else
    printf 'FAIL: %s points below the floor. Add tests for the code that lost coverage; if the drop is deliberate, lower the floor with\n' \
      "$(awk -v p="$p" -v f="$floor" 'BEGIN { printf "%.2f", f - p }')"
    printf '  scripts/coverage-floor.sh raise --to %s --lower --reason "<why>"\nand say why in the commit.\n' "$p"
  fi
  if [ -n "$out" ]; then
    printf '{"percent":%s,"covered":%d,"statements":%d,"generated":%d,"floor":%s,"result":"%s"}\n' "$p" "$c" "$s" "$g" "$floor" "$result" >"$out"
  fi
  [ "$ok" = 1 ]
}

cmd_merge() {
  local p
  [ $# -gt 0 ] || die "no cover profile given"
  for p in "$@"; do [ -r "$p" ] || die "cannot read cover profile $p"; done
  echo "mode: atomic"
  LC_ALL=C awk '
    FNR == 1 && /^mode:/ { next }
    NF == 3 { if (!($1 in n)) order[++k] = $1; n[$1] = $2; c[$1] += $3 }
    END { for (i = 1; i <= k; i++) printf "%s %d %d\n", order[i], n[order[i]], c[order[i]] }' "$@"
}

cmd_raise() {
  local to="" headroom=0.5 lower=0 reason="" profiles=() floor new
  while [ $# -gt 0 ]; do
    case "$1" in
      --to) to=${2:-}; shift 2 || die "--to needs a percentage" ;;
      --headroom) headroom=${2:-}; shift 2 || die "--headroom needs points" ;;
      --lower) lower=1; shift ;;
      --reason) reason=${2:-}; shift 2 || die "--reason needs text" ;;
      -*) die "raise: unknown flag $1" ;;
      *) profiles+=("$1"); shift ;;
    esac
  done
  if [ -z "$to" ]; then
    [ "${#profiles[@]}" -gt 0 ] || die "raise needs --to <percent> or the run's cover profiles"
    local c s g
    read -r c s g < <(tally "${profiles[@]}") || exit 2
    to=$(pct "$c" "$s")
  fi
  [[ "$to" =~ ^[0-9]+(\.[0-9]+)?$ ]] || die "--to must be a percentage (got '$to')"
  [[ "$headroom" =~ ^[0-9]+(\.[0-9]+)?$ ]] || die "--headroom must be a number of points (got '$headroom')"
  [ "$lower" = 0 ] || [ -n "$reason" ] || die "--lower needs --reason \"<why>\": the floor only goes down for a stated reason"
  floor=$(read_floor)
  new=$(awk -v t="$to" -v h="$headroom" 'BEGIN { v = int((t - h) * 100 + 1e-6) / 100; if (v < 0) v = 0; printf "%.2f", v }')
  local cmp
  cmp=$(awk -v a="$new" -v b="$floor" 'BEGIN { print (a > b) ? "up" : (a < b) ? "down" : "same" }')
  case "$cmp" in
    same) echo "coverage-floor: measured ${to}%; the floor is already ${floor}% (measured minus ${headroom}): unchanged" ;;
    up)
      write_floor "$new"
      echo "coverage-floor: raised the floor ${floor}% -> ${new}% (measured ${to}% minus ${headroom}) in ${floor_file#"$root"/}"
      echo "  commit it: git commit -m 'chore(coverage): raise the Go coverage floor to ${new}%' ${floor_file#"$root"/}" ;;
    down)
      if [ "$lower" = 0 ]; then
        echo "coverage-floor: measured ${to}% minus ${headroom} is ${new}%, below the floor ${floor}%: the floor only goes up, unchanged."
        echo "  To lower it on purpose: scripts/coverage-floor.sh raise --to ${to} --lower --reason \"<why>\""
        return 0
      fi
      write_floor "$new"
      printf '# lowered %s: %s%% -> %s%% (measured %s%%): %s\n' "$(date -u +%Y-%m-%d)" "$floor" "$new" "$to" "$reason" >>"$floor_file"
      echo "coverage-floor: LOWERED the floor ${floor}% -> ${new}%; the reason is recorded in ${floor_file#"$root"/}"
      echo "  commit it with the reason: git commit -m 'chore(coverage): lower the Go coverage floor to ${new}%: ${reason}' ${floor_file#"$root"/}" ;;
  esac
}

sub=${1:-}; [ $# -gt 0 ] && shift
case "$sub" in
  measure) cmd_measure "$@" ;;
  check) cmd_check "$@" ;;
  merge) cmd_merge "$@" ;;
  raise) cmd_raise "$@" ;;
  -h | --help | "") sed -n '2,/^set -euo pipefail$/p' "$0" | sed '$d' ;;
  *) die "unknown command '$sub' (measure, check, merge, raise)" ;;
esac
