#!/usr/bin/env bash
# Hermetic self-test of scripts/ci-review-marker.sh (the `Reliant review` required
# check): an exact-head pass marker by a pipeline reviewer passes a branch of this
# repository, nothing passes a fork's head (control-plane finding CPI-1), and the
# head's verdict is its newest bot evidence re-review, else its first marker. A
# fake gh answers from fixed JSON; no network.
set -euo pipefail
src="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ci-review-marker.sh"
fx="$(mktemp -d)"
trap 'rm -rf "$fx"' EXIT
mkdir -p "$fx/bin"
cat > "$fx/bin/gh" << 'GH'
#!/usr/bin/env bash
jqexpr=""; prev=""
for a in "$@"; do [ "$prev" = --jq ] && jqexpr="$a"; prev="$a"; done
case "$*" in
  *"/reviews"*) printf '%s\n' "$REVIEWS" ;;
  "api repos/acme/app/pulls/7"*) printf '%s\n' "$PR" | jq -r "$jqexpr" ;;
  *) echo "unexpected gh: $*" >&2; exit 90 ;;
esac
GH
chmod +x "$fx/bin/gh"
export PATH="$fx/bin:$PATH" GITHUB_REPOSITORY=acme/app
fail() { echo "FAIL: $*" >&2; exit 1; }
head=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
pass="[{\"user\":{\"id\":186876276},\"commit_id\":\"$head\",\"body\":\"<!-- review-passed sha=$head -->\"}]"
# check <want exit> <name> <head repo json> <reviews json>
# LABELS: the pull request's labels, as GitHub lists them (default none).
check() {
  local got=0
  PR="{\"head\":{\"sha\":\"$head\",\"repo\":$3},\"labels\":${LABELS:-[]}}" REVIEWS="$4" bash "$src" pr 7 > "$fx/out" 2>&1 || got=$?
  [ "$got" = "$1" ] || fail "$2: exit $got, want $1: $(cat "$fx/out")"
  echo "ok: $2"
}
check 0 "a branch of this repository with the reviewer's marker passes" '{"full_name":"acme/app"}' "$pass"
check 1 "a branch of this repository without a marker fails" '{"full_name":"acme/app"}' '[]'
check 1 "a fork's head fails even with the reviewer's marker" '{"full_name":"stranger/app"}' "$pass"
grep -q 'a fork' "$fx/out" || fail "the fork refusal does not say why: $(cat "$fx/out")"
check 1 "a deleted fork's head (null repo) fails even with the reviewer's marker" 'null' "$pass"
changes="{\"user\":{\"id\":186876276},\"commit_id\":\"$head\",\"body\":\"<!-- review-changes sha=$head -->\"}"
passed="{\"user\":{\"id\":186876276},\"commit_id\":\"$head\",\"body\":\"<!-- review-passed sha=$head -->\"}"
# One verdict per head: the FIRST marker on the sha decides, and it is immutable
# (forge #1683 got three review-changes and then a review-passed for one head).
check 1 "a pass posted after a review-changes on the same head does not pass" '{"full_name":"acme/app"}' "[$changes,$passed]"
check 0 "a review-changes posted after the head's pass does not fail it" '{"full_name":"acme/app"}' "[$passed,$changes]"
# Evidence re-review (control-plane docs/pr-pipeline.md): the bot's newest review
# that also carries a review-rereview marker for the head is its verdict; the same
# marker from any other reviewer (the owner's account, 225976313) is a later marker.
# rr <user id> <verdict marker> [sha]: a re-review of the head (or of sha).
rr() { local s=${3:-$head}; echo "{\"user\":{\"id\":$1},\"commit_id\":\"$head\",\"body\":\"## $2\\n<!-- $2 sha=$head -->\\n<!-- review-rereview sha=$s of=1 -->\"}"; }
check 0 "the bot's re-reviewed pass after a review-changes passes" '{"full_name":"acme/app"}' "[$changes,$(rr 186876276 review-passed)]"
check 1 "the bot's re-reviewed review-changes after a pass fails" '{"full_name":"acme/app"}' "[$passed,$(rr 186876276 review-changes)]"
check 1 "the newest re-review decides: a re-reviewed pass then a re-reviewed review-changes fails" '{"full_name":"acme/app"}' "[$changes,$(rr 186876276 review-passed),$(rr 186876276 review-changes)]"
check 0 "the newest re-review decides: a re-reviewed review-changes then a re-reviewed pass passes" '{"full_name":"acme/app"}' "[$changes,$(rr 186876276 review-changes),$(rr 186876276 review-passed)]"
check 1 "an owner-account re-review pass marker is ignored" '{"full_name":"acme/app"}' "[$changes,$(rr 225976313 review-passed)]"
check 0 "an owner-account re-review changes marker is ignored" '{"full_name":"acme/app"}' "[$passed,$(rr 225976313 review-changes)]"
check 1 "a re-review marker naming another sha is not a re-review of this head" '{"full_name":"acme/app"}' "[$changes,$(rr 186876276 review-passed bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb)]"
stale="{\"user\":{\"id\":186876276},\"commit_id\":\"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\",\"body\":\"<!-- review-passed sha=$head -->\\n<!-- review-rereview sha=$head of=1 -->\"}"
check 1 "a re-review on another commit is not a verdict of this head" '{"full_name":"acme/app"}' "[$changes,$stale]"
# hold and needs:human: a person owns the PR, so its pass never merges it, read live at the
# merge (forge #2166 merged two minutes after hold was added, 2026-11-12). Any other label
# changes nothing.
LABELS='[{"name":"hold"}]' check 1 "a passed head labelled hold fails" '{"full_name":"acme/app"}' "$pass"
grep -q 'is held (hold)' "$fx/out" || fail "the hold refusal does not say why: $(cat "$fx/out")"
LABELS='[{"name":"review:passed"},{"name":"needs:human"}]' check 1 "a passed head labelled needs:human fails" '{"full_name":"acme/app"}' "$pass"
LABELS='[{"name":"review:passed"},{"name":"merge:queue"}]' check 0 "a passed head with only pipeline labels passes" '{"full_name":"acme/app"}' "$pass"
LABELS='[{"name":"hold-on"},{"name":"hold:pin"}]' check 0 "a label that only starts with hold is not hold" '{"full_name":"acme/app"}' "$pass"
