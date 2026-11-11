#!/usr/bin/env bash
# The reporting half of .github/workflows/forge-pin-check.yml: turns the job's
# step outcomes into one verdict and keeps the rolling issues in step with it.
#
# Verdicts (printed as `verdict: <v>`):
#   pass        scripts/pin-forge.sh ran and succeeded for (reliant main, forge).
#   known-good  the pair needed no run: reliant main already pins that forge
#               commit, or the cache records an earlier pass of the pair.
#   fail        scripts/pin-forge.sh ran and failed: a demonstrated pin
#               incompatibility.
#   error       the check itself could not run (pair resolution, the cache
#               lookup or the toolchain setup failed): nothing was proven.
#
# Issues, each found by its hidden marker among open issues with its label:
#   forge-pin:red     the pair does not pin. pass/known-good close it; fail
#                     opens it or comments on it; error leaves it alone.
#   forge-pin:broken  the monitor cannot run. pass/known-good/fail close it
#                     (the check ran); error opens it or comments on it,
#                     naming the failed stage.
#
# Inputs (environment; the workflow passes its steps' outcomes):
#   PAIR_OUTCOME SEEN_OUTCOME SETUP_OUTCOME PIN_OUTCOME
#                  steps.<id>.outcome: success|failure|cancelled|skipped|""
#   PINNED         "true" when go.mod already pins forge F
#   CACHE_HIT      "true" when the pair's pass marker is cached
#   F R            the forge and reliant commits (may be empty on error)
#   RUN_URL        this run, linked from every comment
#   PIN_LOG        scripts/pin-forge.sh's output (default .forge-pin-check/pin.log)
#   GITHUB_REPOSITORY
# Self-test: scripts/test-forge-pin-report.sh (fake gh, no network).
set -uo pipefail

: "${GITHUB_REPOSITORY:?}"
F="${F:-}" R="${R:-}" RUN_URL="${RUN_URL:-}"
PIN_LOG="${PIN_LOG:-.forge-pin-check/pin.log}"

verdict="" stage=""
if [ "${PAIR_OUTCOME:-}" != success ]; then
  verdict=error stage="resolving the (reliant main, forge) pair"
elif [ "${PINNED:-}" = true ]; then
  verdict=known-good
elif [ "${SEEN_OUTCOME:-}" != success ]; then
  verdict=error stage="looking up the pair's cached pass"
elif [ "${CACHE_HIT:-}" = true ]; then
  verdict=known-good
elif [ "${SETUP_OUTCOME:-}" != success ]; then
  verdict=error stage="setting up the toolchain (.github/actions/setup)"
elif [ "${PIN_OUTCOME:-}" = success ]; then
  verdict=pass
elif [ "${PIN_OUTCOME:-}" = failure ]; then
  verdict=fail
else
  verdict=error stage="running scripts/pin-forge.sh (outcome: ${PIN_OUTCOME:-none})"
fi
echo "verdict: $verdict${stage:+ ($stage)}"

red_mark='<!-- forge-pin-check -->'
broken_mark='<!-- forge-pin-check:broken -->'

# find <label> <marker> -> the open issue's number, or nothing.
find_issue() {
  gh issue list -R "$GITHUB_REPOSITORY" --state open --label "$1" --limit 20 --json number,body 2>/dev/null |
    jq -r --arg m "$2" '[.[] | select(.body | contains($m))][0].number // empty' 2>/dev/null || true
}
# close <label> <marker> <comment>
close_issue() {
  local n
  n="$(find_issue "$1" "$2")"
  [ -z "$n" ] || gh issue close "$n" -R "$GITHUB_REPOSITORY" --comment "$3"
}
# raise <label> <marker> <description> <title> <body>
raise_issue() {
  local n
  n="$(find_issue "$1" "$2")"
  gh label create "$1" -R "$GITHUB_REPOSITORY" --color B60205 --description "$3" >/dev/null 2>&1 || true
  if [ -n "$n" ]; then
    gh issue comment "$n" -R "$GITHUB_REPOSITORY" --body "$5"
  else
    gh issue create -R "$GITHUB_REPOSITORY" --label "$1" --title "$4" --body "$5"
  fi
}

run_link="([run]($RUN_URL))"
case "$verdict" in
  pass|known-good)
    why="pins into"
    [ "$verdict" = known-good ] && why="is known to pin into"
    close_issue forge-pin:red "$red_mark" \
      "forge \`${F:0:12}\` $why reliant main \`${R:0:12}\` again $run_link." || exit 1
    close_issue forge-pin:broken "$broken_mark" "The forge pin check runs again $run_link." || exit 1
    ;;
  fail)
    close_issue forge-pin:broken "$broken_mark" "The forge pin check runs again $run_link." || exit 1
    tail="$(grep -v '^\s*$' "$PIN_LOG" 2>/dev/null | tail -n 40)"
    body="$(printf 'forge main `%s` does not pin into reliant main `%s`: `scripts/pin-forge.sh %s` failed %s.\n\nThe release train runs the same script at every cut, so the next cut will die there until forge or reliant main is fixed.\n\n```\n%s\n```\n\n%s' \
      "${F:0:12}" "${R:0:12}" "$F" "$run_link" "$tail" "$red_mark")"
    raise_issue forge-pin:red "$red_mark" "forge main does not pin into reliant main (forge-pin-check.yml)" \
      "forge main does not pin into reliant main (the next RC cut will fail)" "$body" || exit 1
    ;;
  error)
    body="$(printf 'The forge pin check could not run: it failed while %s %s (forge `%s`, reliant main `%s`).\n\nNothing was proven about whether forge main pins into reliant main; an open forge-pin:red issue, if any, still stands.\n\n%s' \
      "$stage" "$run_link" "${F:0:12}" "${R:0:12}" "$broken_mark")"
    raise_issue forge-pin:broken "$broken_mark" "the forge pin check cannot run (forge-pin-check.yml)" \
      "The forge pin check cannot run" "$body" || exit 1
    ;;
esac
