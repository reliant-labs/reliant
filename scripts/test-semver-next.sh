#!/usr/bin/env bash
# Hermetic test of scripts/semver-next.sh over a throwaway git repository.
set -euo pipefail
SCRIPT="$(cd "$(dirname "$0")" && pwd)/semver-next.sh"
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
fail=0
g() { git -C "$T" -c user.name=t -c user.email=t@t -c commit.gpgsign=false "$@"; }
c() { g commit -q --allow-empty -m "$@"; }
expect() { # <want stdout> <want exit> <args...>
  local want="$1" code="$2" got rc=0; shift 2
  got="$(bash "$SCRIPT" --repo "$T" "$@" 2>"$T.err")" || rc=$?
  if [ "$got" != "$want" ] || [ "$rc" != "$code" ]; then
    echo "FAIL: $* -> '$got' exit $rc (want '$want' exit $code)"; cat "$T.err"; fail=1
  else echo "ok: $* -> '$got' exit $rc"; fi
}

g init -q -b main
c "chore: init"; g tag v1.4.2
c "fix(api): a fix (#1)"; c "docs: words (#2)"; c "not conventional at all"
expect v1.4.3 0
g tag rc/v1.4.3-rc.1; g tag v1.4.3-rc.1   # neither is a base
expect v1.4.3 0
c "feat(cli): a feature (#3)"
expect v1.5.0 0
expect v1.5.0 0 --base v1.4.2 --head HEAD
g tag v1.5.0; c "perf: faster (#4)"
expect v1.5.1 0
c "refactor(core)!: drop the old flag (#5)"
expect "" 3
grep -q '(#5)' "$T.err" || { echo "FAIL: breaking refusal does not name #5"; fail=1; }
g tag -d v1.5.0 >/dev/null; g tag v1.5.0 HEAD   # base past the break
c "fix: x (#6)" -m "BREAKING CHANGE: the y field is gone"
expect "" 3
grep -q '(#6)' "$T.err" || { echo "FAIL: footer refusal does not name #6"; fail=1; }
expect v1.6.0 0 --allow-breaking
expect v1.6.0 0 --breaking-since HEAD
expect "" 3 --breaking-since HEAD~1
c "fix(pkg): x (#6b)" -m "API-BREAK: codemod=x"
expect "" 3 --base-rev HEAD~1
c "feat: mentions BREAKING CHANGE: in prose only (#7)"
expect v1.6.0 0 --base-rev HEAD~1
expect "" 2 --base v9

# A release of a later commit refuses an older head; a tag beside the head's history
# (a release train's pin commit) is still the last release; a version already taken
# by other source refuses.
c "fix: y (#8)"; g tag v1.6.0
expect v1.6.1 0 --head HEAD                    # the head itself is the base
expect "" 4 --head HEAD~1                      # an older head: a descendant shipped
grep -q 'v1.6.0' "$T.err" || { echo "FAIL: descendant refusal does not name v1.6.0"; fail=1; }
expect v1.6.1 0 --head HEAD~1 --no-older-check # the release train's opt-out
c "fix: z (#9)"
g checkout -q -b pin HEAD; c "chore(rc): pin (#10)"; g tag v1.6.1   # the release, beside main
g checkout -q main; c "feat: after the release (#11)"
expect v1.7.0 0                                # counts from v1.6.1, on the pin commit
g checkout -q -b side2 HEAD~3; c "feat: again (#12)"; g checkout -q main
expect "" 4 --base v1.5.0 --head side2 --allow-breaking   # computes v1.6.0, already another commit's
grep -q 'v1.6.0 is already released' "$T.err" || { echo "FAIL: collision refusal does not name v1.6.0"; fail=1; }
# A git log that fails after both ends resolve (a history object is missing) is a
# git error, never a silent patch.
B="$T/broken"; mkdir "$B"
gb() { git -C "$B" -c user.name=t -c user.email=t@t -c commit.gpgsign=false "$@"; }
gb init -q -b main; gb commit -q --allow-empty -m "chore: init"; gb tag v2.0.0
gb commit -q --allow-empty -m "feat: lost (#8)"; mid="$(gb rev-parse HEAD)"
gb commit -q --allow-empty -m "fix: tip (#9)"
rm -f "$B/.git/objects/${mid:0:2}/${mid:2}"
expect "" 2 --repo "$B"

# A reviewed retraction (API-BREAK-RETRACTED: <sha> <reason>) in a newer commit stops
# exactly that breaking commit refusing; the version still bumps a minor.
R="$T/retract"; mkdir "$R"
gr() { git -C "$R" -c user.name=t -c user.email=t@t -c commit.gpgsign=false "$@"; }
gr init -q -b main; gr commit -q --allow-empty -m "chore: init"; gr tag v1.1.0
gr commit -q --allow-empty -m "fix: before the break (#20)"; pre="$(gr rev-parse HEAD)"
gr commit -q --allow-empty -m "revert(pkg): remove it (#21)" -m "API-BREAK: playbook=x"
brk="$(gr rev-parse HEAD)"
gr checkout -q -b ok "$brk"
gr commit -q --allow-empty -m "docs(pkg): deprecate (#22)" -m "API-BREAK-RETRACTED: ${brk:0:12} restored in the same squash"
expect v1.2.0 0 --repo "$R" --head ok
grep -q "note: ${brk:0:12} .* but $(gr rev-parse --short=12 ok) retracts it (API-BREAK-RETRACTED): restored in the same squash" "$T.err" \
  || { echo "FAIL: retraction note does not name the retracted and retracting commits"; cat "$T.err"; fail=1; }
# With --breaking-since before the break, the retraction (in SINCE..HEAD) is what
# clears it: the train's case (forge's previous set predates both commits).
expect v1.2.0 0 --repo "$R" --head ok --breaking-since "$pre"
grep -q "note: ${brk:0:12} .* retracts it (API-BREAK-RETRACTED)" "$T.err" \
  || { echo "FAIL: retraction after --breaking-since does not clear the break"; cat "$T.err"; fail=1; }
# A retraction reachable from --breaking-since is not in SINCE..HEAD and never
# counts; the break it names is then reachable too, so it is judged as carried.
expect v1.2.0 0 --repo "$R" --head ok --breaking-since ok
grep -q "note: ${brk:0:12} .* an earlier release candidate already carried it" "$T.err" \
  && ! grep -q 'retracts it' "$T.err" \
  || { echo "FAIL: a retraction reachable from --breaking-since was honoured"; cat "$T.err"; fail=1; }
# It never covers another breaking commit, later in the same history.
gr commit -q --allow-empty -m "fix: another break (#23)" -m "BREAKING CHANGE: z"
expect "" 3 --repo "$R" --head ok
grep -q '(#23)' "$T.err" && ! grep -q '^  .*(#21)' "$T.err" \
  || { echo "FAIL: want #23 refused and #21 retracted"; cat "$T.err"; fail=1; }
# A retraction naming a different sha (here the commit before the break) still refuses.
gr checkout -q -b wrong "$brk"
gr commit -q --allow-empty -m "docs: wrong sha (#24)" -m "API-BREAK-RETRACTED: ${pre:0:12} not the break"
expect "" 3 --repo "$R" --head wrong
grep -q '(#21)' "$T.err" || { echo "FAIL: wrong-sha retraction does not refuse #21"; fail=1; }
# A retraction shorter than 12 hex chars, or with no reason, is not a retraction.
gr checkout -q -b short "$brk"
gr commit -q --allow-empty -m "docs: short (#25)" -m "API-BREAK-RETRACTED: ${brk:0:11} too short" \
  -m "API-BREAK-RETRACTED: ${brk:0:12}"
expect "" 3 --repo "$R" --head short
# A retraction OLDER than the breaking commit (its ancestor) still refuses. A commit
# cannot name a descendant's sha, so graft the break onto the retraction instead:
# the break keeps its sha and the retraction becomes its parent.
gr checkout -q -b older "$pre"
gr commit -q --allow-empty -m "docs: early retraction (#26)" -m "API-BREAK-RETRACTED: ${brk:0:12} too early"
gr replace --graft "$brk" "$(gr rev-parse older)"
gr checkout -q older; gr reset -q --hard "$brk"
gr merge-base --is-ancestor "$(gr rev-parse ':/early retraction')" "$brk" \
  || { echo "FAIL: graft did not make the retraction an ancestor of the break"; fail=1; }
expect "" 3 --repo "$R" --head older
grep -q '(#21)' "$T.err" || { echo "FAIL: older retraction does not refuse #21"; fail=1; }
exit "$fail"
