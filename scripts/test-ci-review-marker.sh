#!/usr/bin/env bash
# Hermetic self-test of scripts/ci-review-marker.sh (the `Reliant review` required
# check): an exact-head pass marker by a pipeline reviewer passes a branch of this
# repository, nothing passes a fork's head (control-plane finding CPI-1), and the
# head's verdict is its newest bot evidence re-review, else its first marker.
# Approval is this check, never a label: a pass is head-bound (a push invalidates
# it), no label grants one (`review:passed`, `merge:queue` and `review:changes`
# are markers only), and `hold` / `needs:human` only deny. Merge-group mode judges
# every queue entry at or ahead of ours and fails closed on an unreadable queue.
# A fake gh answers from fixed JSON; no network.
set -euo pipefail
src="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ci-review-marker.sh"
fx="$(mktemp -d)"
trap 'rm -rf "$fx"' EXIT
mkdir -p "$fx/bin"
# The fake gh: pull request #n reads PR_<n> / REVIEWS_<n> (#7 falls back to PR /
# REVIEWS); the merge queue's GraphQL query reads QUEUE (its entries nodes, as the
# script's --jq selects them). Anything else is an unexpected call (exit 90).
cat > "$fx/bin/gh" << 'GH'
#!/usr/bin/env bash
set -o pipefail
jqexpr=""; prev=""
for a in "$@"; do [ "$prev" = --jq ] && jqexpr="$a"; prev="$a"; done
n=$(sed -nE 's#.*repos/acme/app/pulls/([0-9]+).*#\1#p' <<<"$*")
pick() { # <PR|REVIEWS>: the fixture for pull request #n
  local v="$1_$n"
  if [ -n "${!v:-}" ]; then printf '%s\n' "${!v}"
  elif [ "$n" = 7 ]; then printf '%s\n' "${!1}"
  else echo "unexpected gh: no $1 fixture for #$n" >&2; exit 90; fi
}
case "$*" in
  "api graphql "*) [ -z "${QUEUE_READ_FAILS:-}" ] || { echo "HTTP 502" >&2; exit 1; }; printf '%s\n' "$QUEUE" | jq -c "$jqexpr" ;;
  *"repos/acme/app/pulls/"*"/reviews"*) pick REVIEWS ;;
  "api repos/acme/app/pulls/"*) [ -z "${PR_READ_FAILS:-}" ] || { echo "HTTP 502" >&2; exit 1; }; pick PR | jq -r "$jqexpr" ;;
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
# The labels are read live: a read that fails (GitHub answered 502) passes nothing.
PR_READ_FAILS=1 check 1 "a pull request that cannot be read is not passed" '{"full_name":"acme/app"}' "$pass"
grep -q 'cannot read the pull request' "$fx/out" || fail "the read failure does not say why: $(cat "$fx/out")"
# Approval is this check, never a label (owner, 2026-11-20). The pipeline's labels
# are markers: none grants a pass, none denies one; only an exact-head marker passes.
LABELS='[{"name":"review:passed"},{"name":"merge:queue"}]' check 1 "review:passed and merge:queue with no marker at the head fail" '{"full_name":"acme/app"}' '[]'
old=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
prev="[{\"user\":{\"id\":186876276},\"commit_id\":\"$old\",\"body\":\"<!-- review-passed sha=$old -->\"}]"
LABELS='[{"name":"review:passed"},{"name":"merge:queue"}]' check 1 "a pass on the previous head, still labelled review:passed, fails (a push invalidates it)" '{"full_name":"acme/app"}' "$prev"
LABELS='[{"name":"review:changes"}]' check 0 "a pass at the head with a stale review:changes label passes" '{"full_name":"acme/app"}' "$pass"
check 1 "a pass marker by anyone but the pipeline's reviewers fails" '{"full_name":"acme/app"}' "[{\"user\":{\"id\":424242},\"commit_id\":\"$head\",\"body\":\"<!-- review-passed sha=$head -->\"}]"
check 1 "an APPROVED review with no marker is not a pass" '{"full_name":"acme/app"}' "[{\"user\":{\"id\":225976313},\"commit_id\":\"$head\",\"state\":\"APPROVED\",\"body\":\"\"}]"
# Merge-group mode: the group for #9 is judged with every queue entry at or ahead of
# it (#8), each at its own live head; entries behind it (#10) are not this group's.
# The fake gh has no fixture for #10, so judging it would fail the group.
grp=cccccccccccccccccccccccccccccccccccccccc
h8=8888888888888888888888888888888888888888
h9=9999999999999999999999999999999999999999
prj() { echo "{\"head\":{\"sha\":\"$1\",\"repo\":{\"full_name\":\"acme/app\"}},\"labels\":[]}"; }
passed_at() { echo "[{\"user\":{\"id\":186876276},\"commit_id\":\"$1\",\"body\":\"<!-- review-passed sha=$1 -->\"}]"; }
queue="[{\"position\":1,\"headCommit\":{\"oid\":\"dddddddddddddddddddddddddddddddddddddddd\"},\"pullRequest\":{\"number\":8}},
        {\"position\":2,\"headCommit\":{\"oid\":\"$grp\"},\"pullRequest\":{\"number\":9}},
        {\"position\":3,\"headCommit\":{\"oid\":\"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee\"},\"pullRequest\":{\"number\":10}}]"
queue_answer="{\"data\":{\"repository\":{\"mergeQueue\":{\"entries\":{\"nodes\":$queue}}}}}"
# gcheck <want exit: a number, or "fail" for any refusal> <name>; QUEUE and the
# PR_<n> / REVIEWS_<n> fixtures come from the environment.
gcheck() {
  local got=0
  bash "$src" group "gh-readonly-queue/main/pr-9-$grp" "$grp" > "$fx/out" 2>&1 || got=$?
  if [ "$1" = fail ]; then [ "$got" != 0 ] || fail "$2: exit 0, want a refusal: $(cat "$fx/out")"
  else [ "$got" = "$1" ] || fail "$2: exit $got, want $1: $(cat "$fx/out")"; fi
  ! grep -q 'unexpected gh' "$fx/out" || fail "$2: an unexpected gh call: $(cat "$fx/out")"
  echo "ok: $2"
}
export PR_8 REVIEWS_8 PR_9 REVIEWS_9 QUEUE
PR_8=$(prj "$h8") REVIEWS_8=$(passed_at "$h8") PR_9=$(prj "$h9") REVIEWS_9=$(passed_at "$h9") QUEUE=$queue_answer \
  gcheck 0 "a merge group whose entries at and ahead of ours all passed at their heads passes"
for want in "#8: pr-review passed its head $h8" "#9: pr-review passed its head $h9"; do
  grep -q "$want" "$fx/out" || fail "the group did not judge ${want%%:*}: $(cat "$fx/out")"
done
PR_8=$(prj "$h8") REVIEWS_8=$(passed_at "$old") PR_9=$(prj "$h9") REVIEWS_9=$(passed_at "$h9") QUEUE=$queue_answer \
  gcheck 1 "a merge group with an entry ahead lacking a pass at its head fails"
grep -q "#8: its head $h8 has no pr-review pass verdict" "$fx/out" || fail "the refusal does not name #8: $(cat "$fx/out")"
PR_8=$(prj "$h8") REVIEWS_8=$(passed_at "$h8") PR_9=$(prj "$h9") REVIEWS_9=$(passed_at "$h9") QUEUE=$queue_answer QUEUE_READ_FAILS=1 \
  gcheck fail "a merge group whose queue cannot be read fails closed"
PR_8=$(prj "$h8") REVIEWS_8=$(passed_at "$h8") PR_9=$(prj "$h9") REVIEWS_9=$(passed_at "$h9") QUEUE='{"data":{"repository":{"mergeQueue":null}}}' \
  gcheck fail "a merge group whose queue answer has no entries fails closed"
PR_8=$(prj "$h8") REVIEWS_8=$(passed_at "$h8") PR_9=$(prj "$h9") REVIEWS_9=$(passed_at "$h9") QUEUE='<html>502 Bad Gateway</html>' \
  gcheck fail "a merge group whose queue answer is not JSON fails closed"
