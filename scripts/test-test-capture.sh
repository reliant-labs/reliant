#!/usr/bin/env bash
# Self-test of scripts/test-capture.sh's Vitest lane: a record captured in one
# checkout reads correctly in ANOTHER checkout of the same commit. That
# handoff is ci.yml's: the release gate records `task test:web` in its private
# snapshot, and the test-upload job reports the snapshot's runs from the
# runner's own checkout.
#
# A fixture project (a git repo with a forge.yaml at reliant's pin and one
# TypeScript file) is checkout A. A fake `vitest` writes what Vitest writes
# into $FORGE_TEST_LANE_DIR: Istanbul coverage-final.json and vitest.json,
# both naming files by ABSOLUTE path under A. Checkout B is a clone of A in
# another directory; it gets A's runs and reports them. The test asserts:
#   - the lane's files name sources repo-relative (web/src/a.ts), not A's path;
#   - `forge test coverage` in B places the file at web/src/a.ts with the git
#     blob id of that file at the commit;
#   - the lane's exit status is the command's.
#
# Needs the pinned forge (scripts/forge.sh resolves it: FORGE_BIN, a forge on
# PATH at the pin, reliant, go run), git and jq.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/.." && pwd)"
fx="$(mktemp -d)"
trap 'rm -rf "$fx"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
g() { git -c user.name=fx -c user.email=fx@example.com -c init.defaultBranch=main "$@"; }

a="$fx/a"
mkdir -p "$a/web/src" "$a/scripts"
pin=$(sed -n 's/^forge_version:[[:space:]]*//p' "$repo/forge.yaml" | head -1)
printf 'name: fx\nforge_version: %s\n' "$pin" >"$a/forge.yaml"
printf 'export const answer = 42;\n' >"$a/web/src/a.ts"
printf '.forge/\n' >"$a/.gitignore"
cp "$repo/scripts/test-capture.sh" "$repo/scripts/forge.sh" "$repo/scripts/go-test.sh" "$a/scripts/"
g -C "$a" init -q && g -C "$a" add -A && g -C "$a" commit -qm fixture
a=$(cd "$a" && pwd -P)

# The fake vitest: coverage and results by absolute path, as Vitest writes
# them, and exit 3 so the lane's status is checked too.
cat >"$fx/vitest" <<'SH'
#!/usr/bin/env bash
src="$PWD/src/a.ts"
cat >"$FORGE_TEST_LANE_DIR/coverage-final.json" <<JSON
{"$src":{"path":"$src","statementMap":{"0":{"start":{"line":1,"column":0},"end":{"line":1,"column":26}}},"fnMap":{},"branchMap":{},"s":{"0":1},"f":{},"b":{}}}
JSON
cat >"$FORGE_TEST_LANE_DIR/vitest.json" <<JSON
{"numTotalTests":1,"testResults":[{"name":"$PWD/src/a.test.ts","status":"passed","assertionResults":[{"ancestorTitles":["a"],"title":"answers","status":"passed","duration":1}]}]}
JSON
exit 3
SH
chmod +x "$fx/vitest"

rc=0
(cd "$a/web" && "$a/scripts/test-capture.sh" vitest frontend "$fx/vitest") >"$fx/capture.log" 2>&1 || rc=$?
[[ "$rc" -eq 3 ]] || fail "the lane exits with the command's status (3), got $rc: $(cat "$fx/capture.log")"
lane=$(ls -d "$a"/.forge/tests/runs/*/frontend 2>/dev/null | head -1)
[[ -n "$lane" ]] || fail "no frontend lane was recorded: $(cat "$fx/capture.log")"
keys=$(jq -r 'keys[]' "$lane/coverage-final.json")
paths=$(jq -r '.[].path' "$lane/coverage-final.json")
names=$(jq -r '.testResults[].name' "$lane/vitest.json")
[[ "$keys $paths $names" == "web/src/a.ts web/src/a.ts web/src/a.test.ts" ]] ||
  fail "the record names files by absolute path (keys: $keys; paths: $paths; tests: $names)"

b="$fx/b"
g clone -q "$a" "$b"
mkdir -p "$b/.forge/tests"
cp -r "$a/.forge/tests/runs" "$b/.forge/tests/"
(cd "$b" && "$b/scripts/forge.sh" test coverage --json --by file) >"$fx/cov.json" 2>"$fx/cov.err" ||
  fail "forge test coverage in the second checkout: $(cat "$fx/cov.err")"
want_blob=$(git -C "$b" rev-parse HEAD:web/src/a.ts)
got=$(jq -r '.coverage.files[] | select(.lang == "ts") | "\(.path) \(.blob_id // "none")"' "$fx/cov.json")
[[ "$got" == "web/src/a.ts $want_blob" ]] ||
  fail "in another checkout the TypeScript file is \"$got\", want \"web/src/a.ts $want_blob\""

echo "test-capture self-test: ok"
