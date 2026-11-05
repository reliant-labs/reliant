#!/usr/bin/env bash
# Hermetic self-test of scripts/forge-pin-report.sh, the reporting half of
# forge-pin-check.yml: each combination of step outcomes yields the right
# verdict, and the rolling forge-pin:red / forge-pin:broken issues are opened,
# commented on and closed accordingly. A fake gh keeps the open issues in a
# JSON file and logs every write; no network. Needs jq.
set -euo pipefail
src="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/forge-pin-report.sh"
fx="$(mktemp -d)"
trap 'rm -rf "$fx"' EXIT
mkdir -p "$fx/bin"
cat >"$fx/bin/gh" <<'GH'
#!/usr/bin/env bash
set -euo pipefail
st="$FX/issues.json"
arg() { local want=$1 prev=""; shift; for a in "$@"; do [ "$prev" = "$want" ] && { printf '%s' "$a"; return; }; prev=$a; done; }
case "$1 $2" in
  "issue list")
    jq --arg l "$(arg --label "$@")" '[.[] | select(.label == $l) | {number, body}]' "$st" ;;
  "issue close")
    echo "close $3" >>"$FX/log"
    jq --argjson n "$3" 'map(select(.number != $n))' "$st" >"$st.new" && mv "$st.new" "$st" ;;
  "issue comment")
    echo "comment $3 $(arg --body "$@" | head -1)" >>"$FX/log" ;;
  "issue create")
    l="$(arg --label "$@")" b="$(arg --body "$@")"
    n=$(( $(jq 'map(.number) | max // 0' "$st") + 1 ))
    echo "create $n $l $(head -1 <<<"$b")" >>"$FX/log"
    jq --argjson n "$n" --arg l "$l" --arg b "$b" '. + [{number: $n, label: $l, body: $b}]' "$st" >"$st.new" &&
      mv "$st.new" "$st" ;;
  "label create") ;;
  *) echo "unexpected gh: $*" >&2; exit 90 ;;
esac
GH
chmod +x "$fx/bin/gh"
export PATH="$fx/bin:$PATH" GITHUB_REPOSITORY=acme/app FX="$fx" RUN_URL=https://run/1 \
  F=ffffffffffffffffffffffffffffffffffffffff R=rrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrr
fail() { echo "FAIL: $*" >&2; exit 1; }

# reset <issues json>; report KEY=VAL... -> the script's stdout; the gh log is $fx/log.
reset() { printf '%s\n' "$1" >"$fx/issues.json"; : >"$fx/log"; }
report() {
  env PAIR_OUTCOME=success PINNED=false SEEN_OUTCOME=success CACHE_HIT=false \
    SETUP_OUTCOME=success PIN_OUTCOME=success PIN_LOG="$fx/pin.log" "$@" bash "$src"
}
red='{"number":5,"label":"forge-pin:red","body":"x\n<!-- forge-pin-check -->"}'
broken='{"number":6,"label":"forge-pin:broken","body":"y\n<!-- forge-pin-check:broken -->"}'
log() { cat "$fx/log"; }

printf 'go: build failed\n\nundefined: forge.Thing\n' >"$fx/pin.log"

# A failed pin with no issue open opens forge-pin:red with the log's tail.
reset '[]'
out="$(report PIN_OUTCOME=failure)"
grep -q '^verdict: fail$' <<<"$out" || fail "a failed pin is a fail: $out"
grep -q '^create 1 forge-pin:red ' "$fx/log" || fail "a fail opens forge-pin:red: $(log)"
jq -e '.[0].body | contains("undefined: forge.Thing") and contains("<!-- forge-pin-check -->")' \
  "$fx/issues.json" >/dev/null || fail "the issue carries the log tail and its marker"

# A second failure comments on the open issue; it does not open another.
out="$(report PIN_OUTCOME=failure)"
grep -q '^comment 1 ' "$fx/log" && [ "$(grep -c '^create ' "$fx/log")" = 1 ] ||
  fail "a repeat fail comments on issue 1: $(log)"

# A pass closes it.
reset "[$red]"
out="$(report)"
grep -q '^verdict: pass$' <<<"$out" || fail "a successful pin is a pass: $out"
[ "$(log)" = "close 5" ] || fail "a pass closes forge-pin:red: $(log)"

# Recovery with no run: the pair is already pinned, or its pass is cached.
reset "[$red]"
out="$(report PINNED=true SEEN_OUTCOME=skipped SETUP_OUTCOME=skipped PIN_OUTCOME=skipped)"
grep -q '^verdict: known-good$' <<<"$out" || fail "an already-pinned pair is known-good: $out"
[ "$(log)" = "close 5" ] || fail "an already-pinned pair closes forge-pin:red: $(log)"
reset "[$red,$broken]"
out="$(report CACHE_HIT=true SETUP_OUTCOME=skipped PIN_OUTCOME=skipped)"
grep -q '^verdict: known-good$' <<<"$out" || fail "a cached pass is known-good: $out"
[ "$(log)" = "$(printf 'close 5\nclose 6')" ] || fail "a cached pass closes both issues: $(log)"

# The monitor cannot run: forge-pin:broken names the stage; forge-pin:red stands.
for c in "PAIR_OUTCOME=failure|resolving the" "SEEN_OUTCOME=failure|looking up" \
  "SETUP_OUTCOME=failure|setting up the toolchain" "PIN_OUTCOME=cancelled|outcome: cancelled"; do
  reset "[$red]"
  out="$(report "${c%%|*}" PIN_OUTCOME="$([ "${c%%|*}" = PIN_OUTCOME=cancelled ] && echo cancelled || echo skipped)")"
  grep -q '^verdict: error (' <<<"$out" && grep -qF "${c#*|}" <<<"$out" || fail "${c%%|*} is an error naming its stage: $out"
  grep -q '^create 6 forge-pin:broken The forge pin check could not run' "$fx/log" ||
    fail "${c%%|*} opens forge-pin:broken: $(log)"
  ! grep -q 'close 5' "$fx/log" || fail "${c%%|*} must not close forge-pin:red: $(log)"
  jq -e "any(.[]; .body | contains(\"${c#*|}\"))" "$fx/issues.json" >/dev/null ||
    fail "the forge-pin:broken issue names the stage of ${c%%|*}"
done
# A repeat error comments; the next real fail closes forge-pin:broken and comments on forge-pin:red.
out="$(report SETUP_OUTCOME=failure PIN_OUTCOME=skipped)"
grep -q '^comment 6 ' "$fx/log" || fail "a repeat error comments on forge-pin:broken: $(log)"
: >"$fx/log"
out="$(report PIN_OUTCOME=failure)"
[ "$(head -1 "$fx/log")" = "close 6" ] && grep -q '^comment 5 ' "$fx/log" ||
  fail "a fail closes forge-pin:broken and updates forge-pin:red: $(log)"

echo "ok: forge-pin-report verdicts and issue transitions"
