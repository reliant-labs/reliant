#!/usr/bin/env bash
# Hermetic test of the HOTFIX release path (control-plane's rc-train hotfix lane): the
# hotfix gate's record as scripts/release-gate.sh check judges it, release-gate.sh hotfix's
# refusals, scripts/release-tag.sh --hotfix (dry run), and scripts/hotfix-bump.sh, over
# throwaway checkouts whose origin is a local bare repository. git, node and jq are real;
# `go` is a stub that writes the notes and the changelog the bump asks it for.
set -uo pipefail
SRC="$(cd "$(dirname "$0")" && pwd)"
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
fails=0
check() { if [ "$2" = "$3" ]; then echo "ok: $1"; else echo "FAIL: $1 (got '$2', want '$3')"; fails=$((fails + 1)); fi; }

mkdir "$T/bin"
cat >"$T/bin/go" <<'EOF'
#!/usr/bin/env bash
# go run ./tools/releasenotes --version V ... --out F | go run ./tools/docgen/changelog/... DIR MDX
# go list -f '{{.Dir}}' ./<dir>... (the hotfix gate's package selection): each directory.
if [ "$1" = list ]; then shift 3; for d in "$@"; do echo "$PWD/${d#./}"; done; exit 0; fi
case "$2" in
  ./tools/releasenotes) out=""; v=""; while [ $# -gt 0 ]; do case "$1" in --out) out="$2" ;; --version) v="$2" ;; esac; shift; done
    printf 'version: "%s"\ntitle: "hotfix"\nsummary: "s"\nsections:\n  - title: "Fixed"\n    items:\n      - title: "the fix"\n' "$v" >"$out" ;;
  ./tools/docgen/changelog/...) ls "$3" >"$4" ;;
  *) exit 2 ;;
esac
EOF
# `task test -- <pkgs>` (the hotfix gate's test-go step): logged; RG_TEST_TASK_RC fails it.
cat >"$T/bin/task" <<'EOF'
#!/usr/bin/env bash
echo "task $*" >>"$RG_TEST_LOG"; exit "${RG_TEST_TASK_RC:-0}"
EOF
chmod +x "$T/bin/go" "$T/bin/task"
export PATH="$T/bin:$PATH" GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 RELEASE_GATE_PUBLISH=0
# The hotfix gate's runner: its private checkout under $T, the pinned forge stubbed, and
# what its steps were asked to run logged to $RG_TEST_LOG.
export RELEASE_GATE_DIR="$T/rg" FORGE_BIN=/bin/true RG_TEST_LOG="$T/steps.log"

R="$T/reliant"; O="$T/origin.git"
git init -q --bare -b main "$O"
git init -q -b main "$R"
mkdir -p "$R/scripts" "$R/electron" "$R/docs/data/releases"
cp "$SRC/release-gate.sh" "$SRC/release-tag.sh" "$SRC/release-lock.sh" "$SRC/rc-shape.sh" "$SRC/hotfix-bump.sh" "$R/scripts/"
# The merge gate the hotfix gate's merge step runs: logs how it was asked.
printf '#!/usr/bin/env bash\necho "merge-gate MG_REACH=$MG_REACH $*" >>"$RG_TEST_LOG"\n' >"$R/scripts/merge-gate.sh"
pkg() { printf '{\n  "name": "fixture",\n  "version": "%s"\n}\n' "$1" >"$R/electron/package.json"; }
pkg 1.24.0; echo changelog >"$R/docs/changelog.mdx"
printf 'version: "v1.24.0"\n' >"$R/docs/data/releases/v1.24.0.yaml"
git -C "$R" add -A; git -C "$R" commit -q -m "chore: release v1.24.0"
git -C "$R" tag v1.24.0
M0="$(git -C "$R" rev-parse HEAD)"
git -C "$R" remote add origin "$O"
# main moves on and declares the next release.
echo next >"$R/next.go"; pkg 1.25.0; git -C "$R" add -A; git -C "$R" commit -q -m "feat: next (#2)"
git -C "$R" push -q origin main --tags

# The hotfix: prod's M0, one pick, then hotfix-bump.sh's commit, on release/v1.24.1-rc.1.
git -C "$R" checkout -q -b hf "$M0"
mkdir -p "$R/pkg/fix"; echo fix >"$R/fix.go"; echo fix >"$R/pkg/fix/fix.go"; git -C "$R" add -A; git -C "$R" commit -q -m "fix: the fix (#3)" -m "RC-Set: v1.24.1-rc.1"
echo "-- hotfix-bump.sh"
(cd "$R" && bash scripts/hotfix-bump.sh v1.24.0) >/dev/null 2>&1; check "a version not above the release HEAD contains is refused" "$?" 1
echo dirty >"$R/dirty"; (cd "$R" && bash scripts/hotfix-bump.sh v1.24.1) >/dev/null 2>&1; check "a dirty checkout is refused" "$?" 1; rm "$R/dirty"
changed="$(cd "$R" && bash scripts/hotfix-bump.sh v1.24.1 2>/dev/null | sort | paste -sd, -)"
check "the bump writes the version, the notes and the changelog, nothing else" "$changed" "docs/changelog.mdx,docs/data/releases/v1.24.1.yaml,electron/package.json"
check "the version is the hotfix's" "$(node -p "require('$R/electron/package.json').version")" 1.24.1
git -C "$R" add -A; git -C "$R" commit -q -m "chore(release): hotfix v1.24.1" -m "RC-Set: v1.24.1-rc.1"
H="$(git -C "$R" rev-parse HEAD)"
git -C "$R" push -q origin "$H:refs/heads/release/v1.24.1-rc.1"

note() { # <sha> <json>
  git -C "$R" notes --ref=release-gate add -f -m "$2" "$1" 2>/dev/null
  git -C "$R" push -q -f origin refs/notes/release-gate:refs/notes/release-gate
}
gate() { (cd "$R" && bash scripts/release-gate.sh "$@") >"$T/gate.out" 2>&1; local rc=$?; grep -E "release-gate: " "$T/gate.out" | tail -1 >&2; echo $rc; }

echo "-- release-gate.sh check: a hotfix record stands only on a green base"
check "no record at all: 3" "$(gate check "$H")" 3
note "$H" "{\"sha\":\"$H\",\"result\":\"PASS\",\"kind\":\"hotfix\",\"base\":\"$M0\",\"finished\":\"f\",\"steps\":[]}"
check "a passing hotfix gate whose base has no record: 3" "$(gate check "$H")" 3
check "and it says why" "$(grep -c "base $(git -C "$R" rev-parse --short "$M0") is not green" "$T/gate.out")" 1
note "$M0" "{\"sha\":\"$M0\",\"result\":\"PASS\",\"finished\":\"f\",\"steps\":[]}"
check "on a green base: 0" "$(gate check "$H")" 0
note "$H" "{\"sha\":\"$H\",\"result\":\"PASS\",\"kind\":\"hotfix\",\"base\":\"$(git -C "$R" rev-parse origin/main)\",\"finished\":\"f\",\"steps\":[]}"
check "a base that is not its ancestor: 3" "$(gate check "$H")" 3
note "$H" "{\"sha\":\"$H\",\"result\":\"FAIL\",\"kind\":\"hotfix\",\"base\":\"$M0\",\"finished\":\"f\",\"steps\":[{\"name\":\"merge\",\"result\":\"FAIL\"}]}"
check "a red hotfix gate: 1" "$(gate check "$H")" 1
note "$M0" "{\"sha\":\"$M0\",\"result\":\"FAIL\",\"finished\":\"f\",\"steps\":[]}"
note "$H" "{\"sha\":\"$H\",\"result\":\"PASS\",\"kind\":\"hotfix\",\"base\":\"$M0\",\"finished\":\"f\",\"steps\":[]}"
check "on a red base: 3" "$(gate check "$H")" 3

echo "-- release-gate.sh hotfix refuses what is not prod's green plus picks"
check "no base: usage" "$(gate hotfix "$H")" 2
check "a red base" "$(gate hotfix --base "$M0" "$H")" 2
note "$M0" "{\"sha\":\"$M0\",\"result\":\"PASS\",\"finished\":\"f\",\"steps\":[]}"
check "a base that is not an ancestor" "$(gate hotfix --base "$(git -C "$R" rev-parse origin/main)" "$H")" 2
check "the commit itself as its base" "$(gate hotfix --base "$H" "$H")" 2

tag() { (cd "$R" && bash scripts/release-tag.sh "$@" --dry-run) >"$T/tag.out" 2>&1; local rc=$?; grep -E "Error|REFUSING" "$T/tag.out" | head -1 >&2; echo $rc; }
echo "-- release-tag.sh --hotfix"
check "a hotfix version main does not declare: refused without --hotfix" "$(tag v1.24.1 --commit "$H" --rc)" 1
check "with --hotfix: tagged (dry run)" "$(tag v1.24.1 --commit "$H" --rc --hotfix)" 0
check "and its gate was the hotfix record" "$(grep -c 'its hotfix gate passed' "$T/tag.out")" 1
check "--hotfix needs --rc" "$(tag v1.24.1 --commit "$H" --hotfix)" 1
check "a version the RC head does not carry" "$(tag v1.24.2 --commit "$H" --rc --hotfix)" 1
check "a version not above a release it contains" "$(tag v1.23.9 --commit "$H" --rc --hotfix)" 1
check "and it says so" "$(grep -c 'is not above v1.24.0' "$T/tag.out")" 1
git -C "$R" push -q origin ":refs/heads/release/v1.24.1-rc.1"
check "a head of no release branch" "$(tag v1.24.1 --commit "$H" --rc --hotfix)" 1

echo "-- release-gate.sh hotfix runs the narrow gate and records it"
unrecord() { # <sha>: no record of it anywhere
  git -C "$R" notes --ref=release-gate remove --ignore-missing "$1" 2>/dev/null
  git -C "$R" push -q -f origin refs/notes/release-gate:refs/notes/release-gate
  git -C "$R" update-ref -d refs/notes/release-gate-origin 2>/dev/null
  rm -f "$(cd "$R" && cd "$(git rev-parse --git-common-dir)" && pwd)/release-gate/$1.json"
}
show() { (cd "$R" && bash scripts/release-gate.sh show "$1") | jq -r "$2"; }
unrecord "$H"; : >"$RG_TEST_LOG"
check "on a green base: 0" "$(gate hotfix --base "$M0" "$H")" 0
check "recorded as a hotfix gate on its base, every hotfix step passing" \
  "$(show "$H" '[.result, .kind, .base, (.steps | map(.name + ":" + .result) | join(","))] | join(" ")')" "PASS hotfix $M0 npm:PASS,merge:PASS,test-go:PASS"
check "the merge gate ran against the base, every dependent included" "$(grep -c "^merge-gate MG_REACH=all --base $M0\$" "$RG_TEST_LOG")" 1
check "the Go tests ran the packages the picks changed, and no other" "$(grep '^task test -- ' "$RG_TEST_LOG" | tr ' ' '\n' | grep -c -e '^./pkg/fix$' -e next)" 1
check "and it is green" "$(gate check "$H")" 0
gate hotfix --base "$M0" "$H" >/dev/null 2>&1
check "the same hotfix gate again: its record is reused" "$(grep -c 'already has a green record' "$T/gate.out")" 1
gate run "$H" >/dev/null 2>&1
check "a full run is not answered by the hotfix record" "$(grep -c 'already has a green record' "$T/gate.out")" 0
unrecord "$H"; : >"$RG_TEST_LOG"
check "a failing step: 1" "$(RG_TEST_TASK_RC=1 gate hotfix --base "$M0" "$H")" 1
check "recorded red, naming the step" "$(show "$H" '[.result, .kind, (.steps | map(.name + ":" + .result) | join(","))] | join(" ")')" "FAIL hotfix npm:PASS,merge:PASS,test-go:FAIL"
check "and check says red" "$(gate check "$H")" 1

[ "$fails" -eq 0 ] && echo "all hotfix release cases passed" || { echo "$fails case(s) failed"; exit 1; }
