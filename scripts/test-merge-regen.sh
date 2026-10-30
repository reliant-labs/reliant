#!/usr/bin/env bash
# test-merge-regen.sh — scripts/merge-regen.sh against throwaway git repos.
# A fake generator stands in for forge: gen/out_gen.txt is a pure function of
# in/*.txt with a `forge:hash=` header over its body, so two PRs that change
# different inputs conflict on exactly that header line, as forge's renders do.
#
#   scripts/test-merge-regen.sh        (seconds; needs only git, bash, jq)
set -euo pipefail
script=$(cd "$(dirname "$0")" && pwd)/merge-regen.sh
tmp=$(mktemp -d "${TMPDIR:-/tmp}/test-merge-regen.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
export MERGE_REGEN_DRIVER_INSTALL=true # the test exercises the no-driver path, the server-side one
export MERGE_REGEN_GENERATE='sh ./gen.sh'
export MERGE_REGEN_GEN_RE='^gen/'

# gen.sh: the "pinned generator".
setup() {
  rm -rf "$tmp/origin.git" "$tmp/w"
  git init -q --bare -b main "$tmp/origin.git"
  git clone -q "$tmp/origin.git" "$tmp/w" 2>/dev/null
  cd "$tmp/w"
  mkdir -p in gen
  cat >gen.sh <<'EOF'
set -e
body=$(for f in in/*.txt; do echo "== $f"; cat "$f"; done)
h=$(printf '%s\n' "$body" | sha256sum | cut -c1-16)
printf '# Code generated. DO NOT EDIT.\n# forge:hash=%s\n%s\n' "$h" "$body" >gen/out_gen.txt
EOF
  printf 'a1\na2\na3\na4\n' >in/a.txt
  printf 'b1\nb2\nb3\nb4\n' >in/b.txt
  sh gen.sh
  git add -A && git commit -qm base && git push -q origin main
}
edit() { # branch file line text
  git checkout -q -B "$1" origin/main
  sed -i "s/^$3\$/$4/" "$2"
  sh gen.sh
  git commit -qam "$1" && git push -q -f origin "$1"
}
run() { (cd "$tmp/w" && bash ${MR_TRACE:+-x} "$script" "$@") }

# 1. Two PRs changing different inputs conflict on the hash line only; the
#    script resolves the PR to exactly the generated tree of the merged inputs.
setup
edit pr in/b.txt b3 B3
edit main in/a.txt a2 A2
git fetch -q origin
git merge-tree --write-tree origin/main origin/pr >/dev/null 2>&1 && fail "precondition: the PRs should conflict"
out=$(run --branch pr --push 2>&1) || fail "resolve exited $?: $out"
echo "$out" | tail -1 | grep -q '^merge-regen: pushed ' || fail "expected pushed, got: $out"
git fetch -q origin
git checkout -q --detach origin/pr
git merge-base --is-ancestor origin/main origin/pr || fail "pr does not contain main"
grep -q '^A2$' in/a.txt && grep -q '^B3$' in/b.txt || fail "merged inputs lost"
want=$(sh gen.sh && cat gen/out_gen.txt); git checkout -q -- gen
[ "$(git show origin/pr:gen/out_gen.txt)" = "$want" ] || fail "gen/out_gen.txt is not the render of the merged inputs"
git grep -q '^<<<<<<<' origin/pr && fail "conflict markers pushed"
git log -1 --format=%s origin/pr | grep -q '^chore: regenerate on merge with main' || fail "commit subject"
echo "ok   generated-only conflict resolves to the generated tree"

# 2. Idempotent: run again, nothing to do.
out=$(run --branch pr --push) || fail "rerun exited $?: $out"
echo "$out" | tail -1 | grep -q '^merge-regen: no-drift ' || fail "rerun should be no-drift, got: $out"
echo "ok   rerun is a no-op"

# 3. A hand-written conflict is refused, and nothing is pushed.
setup
edit pr in/a.txt a2 PR
edit main in/a.txt a2 MAIN
before=$(git rev-parse origin/pr)
set +e; out=$(run --branch pr --push); rc=$?; set -e
[ $rc = 2 ] || fail "hand-written conflict: exit $rc, want 2: $out"
echo "$out" | tail -1 | grep -q '^merge-regen: refused-conflict in/a.txt' || fail "refusal names the file: $out"
git fetch -q origin
[ "$(git rev-parse origin/pr)" = "$before" ] || fail "a refused run pushed"
echo "ok   hand-written conflict is refused"

# 4. A clean merge with a current generate reports no-drift and pushes nothing.
setup
printf 'c\n' >notes.md && git add notes.md && git commit -qm notes && git push -q origin main
edit pr in/b.txt b1 B1
before=$(git rev-parse origin/pr)
out=$(run --branch pr --push) || fail "clean exited $?: $out"
echo "$out" | tail -1 | grep -q '^merge-regen: no-drift ' || fail "clean merge should be no-drift: $out"
git fetch -q origin; [ "$(git rev-parse origin/pr)" = "$before" ] || fail "no-drift pushed"
echo "ok   clean merge without drift pushes nothing"

# 5. A stale generated file on main (a PR merged without regenerating) is
#    regenerated into the PR even though the merge itself is clean.
setup
edit pr in/b.txt b4 B4
git checkout -q -B main origin/main; sed -i 's/^a4$/A4/' in/a.txt; git commit -qam stale; git push -q origin main
out=$(run --branch pr) || fail "stale exited $?: $out"
echo "$out" | tail -1 | grep -q '^merge-regen: would-push ' || fail "stale main should be repaired: $out"
echo "ok   stale generated output on main is regenerated (dry run)"

# 6. Generate that rewrites a hand-written file is refused.
setup
edit pr in/b.txt b2 B2
MERGE_REGEN_GENERATE='sh ./gen.sh && echo drift >>in/a.txt' run --branch pr --push >"$tmp/out" && fail "drift into hand-written files was accepted"
tail -1 "$tmp/out" | grep -q '^merge-regen: refused-drift in/a.txt' || fail "drift refusal: $(cat "$tmp/out")"
echo "ok   generate touching hand-written files is refused"

# 7. --out builds the same commit without pushing: the ref names it in the
#    caller's repo after the worktree is gone, and base/head are recorded.
setup
edit pr in/b.txt b3 B3
edit main in/a.txt a2 A2
before=$(git rev-parse origin/pr)
out=$(run --branch pr --out refs/merge-regen/out 2>&1) || fail "--out exited $?: $out"
new=$(echo "$out" | tail -1 | sed -n 's/^merge-regen: ready //p')
[ -n "$new" ] || fail "--out should print ready: $out"
[ "$(git rev-parse refs/merge-regen/out)" = "$new" ] || fail "--out ref does not name the commit"
git cat-file -e "$new^{commit}" || fail "the commit did not survive the worktree"
git fetch -q origin; [ "$(git rev-parse origin/pr)" = "$before" ] || fail "--out pushed"
[ "$(git rev-parse refs/merge-regen/head)" = "$before" ] || fail "refs/merge-regen/head is not the head used"
[ "$(git rev-parse refs/merge-regen/base)" = "$(git rev-parse origin/main)" ] || fail "refs/merge-regen/base is not the base used"
git show "$new":in/a.txt | grep -q '^A2$' || fail "--out commit lacks main"
git push -q origin "$new:refs/heads/pr" --force-with-lease="refs/heads/pr:$(git rev-parse refs/merge-regen/head)" || fail "caller push of the --out commit"
out=$(run --branch pr --out refs/merge-regen/out 2>&1) || fail "--out rerun: $out"
echo "$out" | tail -1 | grep -q '^merge-regen: no-drift ' || fail "--out rerun after the caller pushed should be no-drift: $out"
set +e; (cd "$tmp/w" && "$script" --branch pr --push --out refs/x) >/dev/null 2>&1; rc=$?; set -e
[ $rc = 1 ] || fail "--push with --out should be refused, exit $rc"
echo "ok   --out leaves the commit in a ref for the caller to push"

# 8. forge's driver makes the LOCAL merge clean and generate is a no-op, but
#    GitHub's merge (no driver) still conflicts: the merge is pushed anyway.
setup
printf 'gen/list_gen.txt merge=forge-generated\n' >.gitattributes
printf 'm\nz\n' >gen/list_gen.txt
git add -A && git commit -qm list && git push -q origin main
edit pr gen/list_gen.txt m m-pr
edit main gen/list_gen.txt m m-main
git fetch -q origin
git merge-tree --write-tree origin/main origin/pr >/dev/null 2>&1 && fail "precondition: a driver-less merge should conflict"
out=$(MERGE_REGEN_DRIVER_INSTALL='git config merge.forge-generated.driver "git merge-file --union %A %O %B"' \
  MERGE_REGEN_GENERATE=true run --branch pr --push 2>&1) || fail "driver-clean exited $?: $out"
echo "$out" | tail -1 | grep -q '^merge-regen: pushed ' || fail "a PR GitHub cannot merge must be pushed even when the driver merges it: $out"
git fetch -q origin
git merge-tree --write-tree origin/main origin/pr >/dev/null 2>&1 || fail "the pushed PR still conflicts for a driver-less merge"
echo "ok   driver-clean but server-conflicting merge is pushed"

# 9. A hand-written file in a directory that also holds generated output is
#    hand-written: with the default classifier its conflict is refused.
setup
mkdir -p deploy/observability
printf 'one\ntwo\nthree\n' >deploy/observability/gateway-sync.sh
git add -A && git commit -qm sync && git push -q origin main
edit pr deploy/observability/gateway-sync.sh two PR
edit main deploy/observability/gateway-sync.sh two MAIN
before=$(git rev-parse origin/pr)
set +e; out=$(cd "$tmp/w" && env -u MERGE_REGEN_GEN_RE "$script" --branch pr --push 2>&1); rc=$?; set -e
[ $rc = 2 ] || fail "deploy/observability hand-written conflict: exit $rc, want 2: $out"
echo "$out" | tail -1 | grep -q 'refused-conflict deploy/observability/gateway-sync.sh' || fail "refusal: $out"
git fetch -q origin; [ "$(git rev-parse origin/pr)" = "$before" ] || fail "a refused run pushed"
echo "ok   hand-written file beside generated output is refused"
echo PASS
