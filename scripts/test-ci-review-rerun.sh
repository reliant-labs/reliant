#!/usr/bin/env bash
# Hermetic self-test of scripts/ci-review-rerun.sh: a passed head's red and
# cancelled `Reliant review` runs are re-run one at a time until none is left,
# and nothing else is ever re-run. A fake gh keeps the head's runs in a JSON
# file and logs every re-run with how many other re-runs were in flight at that moment;
# no network.
set -euo pipefail
src="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ci-review-rerun.sh"
fx="$(mktemp -d)"
trap 'rm -rf "$fx"' EXIT
mkdir -p "$fx/bin"
cat > "$fx/bin/gh" << 'GH'
#!/usr/bin/env bash
set -euo pipefail
jqexpr="."; prev=""
for a in "$@"; do [ "$prev" = --jq ] && jqexpr="$a"; prev="$a"; done
st="$FX/state.json"
save() { jq "$@" "$st" > "$st.new" && mv "$st.new" "$st"; }
case "$*" in
  *"/actions/workflows/reliant-review.yml/runs?head_sha=$HEAD"*)
    jq '{workflow_runs: .runs}' "$st" | jq -r "$jqexpr" ;;
  "api repos/acme/app/actions/runs/"*)
    id=${2##*/}
    echo "get $id" >> "$FX/log"
    # An in-flight run completes after its remaining ticks of polling.
    save --argjson id "$id" '.runs |= map(if .id == $id and .status != "completed" then
      (.ticks -= 1) | if .ticks <= 0 then .status = "completed" | .conclusion = .next else . end else . end)'
    jq --argjson id "$id" '.runs[] | select(.id == $id)' "$st" | jq -r "$jqexpr" ;;
  "run rerun "*)
    id=$3
    [ "$(jq --argjson id "$id" '.runs[] | select(.id == $id) | .status' "$st")" = '"completed"' ] ||
      { echo "run $id is still in flight" >&2; exit 1; }
    # A re-run still in flight (the caller's own run is in flight too, legitimately).
    busy=$(jq '[.runs[] | select(.status != "completed" and .run_attempt > 1)] | length' "$st")
    echo "rerun $id busy=$busy" >> "$FX/log"
    save --argjson id "$id" --arg to "${RERUN_TO:-success}" '.runs |= map(if .id == $id then
      .status = "in_progress" | .conclusion = null | .run_attempt += 1 | .ticks = 2 | .next = $to else . end)' ;;
  "api repos/acme/app/pulls/7"*) printf '%s\n' "$PR" | jq -r "$jqexpr" ;;
  *"repos/acme/app/pulls?state=open"*) printf '%s\n' "$PULLS" | jq -r "$jqexpr" ;;
  *) echo "unexpected gh: $*" >&2; exit 90 ;;
esac
GH
cat > "$fx/gate" << 'GATE'
#!/usr/bin/env bash
exit "${GATE_EXIT:-0}"
GATE
chmod +x "$fx/bin/gh" "$fx/gate"
head=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
export PATH="$fx/bin:$PATH" GITHUB_REPOSITORY=acme/app FX="$fx" HEAD="$head" \
  REVIEW_GATE="$fx/gate" RERUN_POLL_SECONDS=0 RERUN_WAIT_SECONDS=30
export PR="{\"state\":\"open\",\"head\":{\"sha\":\"$head\",\"repo\":{\"full_name\":\"acme/app\"}}}"
fail() { echo "FAIL: $*" >&2; exit 1; }

# run <id> <status> <conclusion> [<attempt>] [<event>] [<ticks>] [<next>]
run() {
  printf '{"id":%s,"created_at":"2026-11-02T00:00:%02dZ","status":"%s","conclusion":%s,"run_attempt":%s,"event":"%s","ticks":%s,"next":"%s"}' \
    "$1" "$1" "$2" "$( [ "$3" = - ] && echo null || echo "\"$3\"")" "${4:-1}" "${5:-pull_request}" "${6:-0}" "${7:-success}"
}
# setup <run json>...: the head's runs, and an empty log
setup() {
  local IFS=,
  echo "{\"runs\":[$*]}" > "$fx/state.json"
  : > "$fx/log"
}
reruns() { grep '^rerun' "$fx/log" | cut -d' ' -f2 | paste -sd' ' -; }
conclusions() { jq -r '[.runs[] | "\(.id):\(.status)/\(.conclusion)"] | join(" ")' "$fx/state.json"; }
heal() { bash "$src" "$@" > "$fx/out" 2>&1 || fail "ci-review-rerun.sh $*: exit $?: $(cat "$fx/out")"; }

setup "$(run 1 completed cancelled)" "$(run 2 completed failure)" "$(run 3 completed timed_out)" "$(run 9 in_progress -)"
heal pr 7 "$head" 9
[ "$(reruns)" = "1 2 3" ] || fail "the green run re-runs every earlier red verdict, oldest first: got '$(reruns)'"
grep -q 'busy=[^0]' "$fx/log" && fail "a re-run started while another was in flight: $(cat "$fx/log")"
[ "$(conclusions)" = "1:completed/success 2:completed/success 3:completed/success 9:in_progress/null" ] ||
  fail "every red verdict is green after the heal: $(conclusions)"
echo "ok: the green run re-runs its head's red and cancelled runs one at a time"

setup "$(run 1 completed failure)" "$(run 2 in_progress - 1 pull_request 99 failure)" "$(run 9 in_progress -)"
heal pr 7 "$head" 9
[ "$(reruns)" = 1 ] || fail "the green run re-runs only completed runs: got '$(reruns)'"
grep -q '^get 2$' "$fx/log" && fail "the green run waited on a run still in flight, which may be waiting on it"
echo "ok: the green run never waits on another run in flight"

setup "$(run 1 completed success)" "$(run 2 in_progress - 1 pull_request 3 failure)"
heal pr 7 "$head"
[ "$(reruns)" = 2 ] || fail "the re-run workflow waits for a run in flight and re-runs it when it ends red: got '$(reruns)'"
[ "$(conclusions)" = "1:completed/success 2:completed/success" ] || fail "the late red run is green: $(conclusions)"
echo "ok: the re-run workflow waits for a run in flight that ends red, then re-runs it"

setup "$(run 1 completed cancelled)" "$(run 2 completed success)"
GATE_EXIT=1 heal pr 7 "$head"
[ -z "$(reruns)" ] || fail "a head pr-review has not passed is never re-run: got '$(reruns)'"
echo "ok: a head that does not pass the gate keeps its red verdicts"

setup "$(run 1 completed cancelled)" "$(run 2 completed success)"
heal pr 7 bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
[ -z "$(reruns)" ] || fail "a head the pull request has moved off is never re-run: got '$(reruns)'"
echo "ok: an old head is left alone"

setup "$(run 1 completed failure 1 merge_group)" "$(run 2 completed success)"
heal pr 7 "$head"
[ -z "$(reruns)" ] || fail "a merge-group run is never re-run: got '$(reruns)'"
echo "ok: merge-group runs are untouched"

setup "$(run 1 completed failure)" "$(run 2 completed cancelled)" "$(run 9 completed success)"
RERUN_TO=failure heal pr 7 "$head" 9
[ "$(reruns)" = 1 ] || fail "a re-run that ends red stops the heal: got '$(reruns)'"
echo "ok: a re-run that ends red again stops the heal"

setup "$(run 1 completed failure 4)" "$(run 9 completed success)"
heal pr 7 "$head" 9
[ -z "$(reruns)" ] || fail "a run already re-run RERUN_MAX_ATTEMPTS times is left alone: got '$(reruns)'"
echo "ok: re-runs per run are bounded"

setup "$(run 1 completed failure)" "$(run 9 completed success)"
PR="{\"state\":\"open\",\"head\":{\"sha\":\"$head\",\"repo\":{\"full_name\":\"stranger/app\"}}}" heal pr 7 "$head" 9
[ -z "$(reruns)" ] || fail "a fork's head is never re-run: got '$(reruns)'"
echo "ok: a fork's head is left alone"

setup "$(run 1 completed cancelled)" "$(run 2 completed cancelled)" "$(run 3 completed success)"
PULLS="[{\"number\":7,\"draft\":false,\"head\":{\"sha\":\"$head\"}}]" heal sweep
[ "$(reruns)" = "1 2" ] || fail "the sweep heals every open pull request's passed head: got '$(reruns)'"
grep -q 'busy=[^0]' "$fx/log" && fail "the sweep started a re-run while another was in flight: $(cat "$fx/log")"
echo "ok: the scheduled sweep heals an open pull request's passed head"
