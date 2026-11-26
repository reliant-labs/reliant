#!/usr/bin/env bash
# test-coverage-floor.sh — the hermetic self-test of scripts/coverage-floor.sh
# (the same pair of files in reliant and control-plane). Runs in a scratch copy:
# the union counts a block once and covered when any profile hit it, generated
# code is left out of the denominator, check passes at the floor and fails below
# it, and raise moves the floor only up unless --lower comes with a reason.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/scripts"
cp "$here/coverage-floor.sh" "$tmp/scripts/"
cf="$tmp/scripts/coverage-floor.sh"
fail() { echo "FAIL: $*" >&2; exit 1; }
expect() { # expect <name> <want substring> <got>
  case "$3" in *"$2"*) ;; *) fail "$1: want '$2' in: $3" ;; esac
}

# Two invocations of one package (a unit and an integration lane): a.go's first
# block is hit only by the second, its second by neither; b.go only in the first.
# gen/, *_gen.go, *.pb.go and testdata/ are generated: never counted.
cat >"$tmp/unit.out" <<'EOF'
mode: atomic
example.com/m/a.go:1.1,2.2 3 0
example.com/m/a.go:3.1,4.2 1 0
example.com/m/b.go:1.1,2.2 4 7
example.com/m/gen/x.go:1.1,2.2 50 0
example.com/m/mock_gen.go:1.1,2.2 50 0
example.com/m/api/v1/api.pb.go:1.1,2.2 50 0
example.com/m/testdata/t.go:1.1,2.2 50 0
EOF
cat >"$tmp/integration.out" <<'EOF'
mode: atomic
example.com/m/a.go:1.1,2.2 3 2
example.com/m/a.go:3.1,4.2 1 0
EOF

# 7 of 8 statements: a.go's 3 (the integration lane's hit) + b.go's 4; a.go's 1 never.
got=$("$cf" measure --json "$tmp/unit.out" "$tmp/integration.out")
[ "$got" = '{"percent":87.50,"covered":7,"statements":8,"generated":200}' ] || fail "measure: got $got"
# Last-writer-wins would read the integration lane alone: 3 of 4. The order must not matter.
got=$("$cf" measure --json "$tmp/integration.out" "$tmp/unit.out")
expect "measure, reversed" '"percent":87.50' "$got"
# merge is the same union as one profile.
"$cf" merge "$tmp/unit.out" "$tmp/integration.out" >"$tmp/merged.out"
expect "merge" '"percent":87.50' "$("$cf" measure --json "$tmp/merged.out")"

# check: at the floor passes, below it fails (exit 1), with the numbers and the way out.
printf '# a comment\n\n87.5\n' >"$tmp/coverage-floor.txt"
out=$("$cf" check --json-out "$tmp/c.json" "$tmp/unit.out" "$tmp/integration.out") || fail "check at the floor failed: $out"
expect "check pass" "floor:    87.5%" "$out"
expect "check pass" "task coverage:raise-floor" "$out"
expect "check json" '"result":"PASS"' "$(cat "$tmp/c.json")"
rc=0; out=$("$cf" check "$tmp/unit.out") || rc=$?
[ "$rc" = 1 ] || fail "check below the floor: exit $rc, want 1: $out"
expect "check fail" "FAIL: 37.50 points below the floor" "$out"
expect "check fail" "--lower --reason" "$out"
rc=0; "$cf" check >/dev/null 2>&1 || rc=$?
[ "$rc" = 2 ] || fail "check with no profile: exit $rc, want 2"
printf 'example.com/m/a.go: not a profile\n' >"$tmp/bad.out"
rc=0; "$cf" check "$tmp/bad.out" >/dev/null 2>&1 || rc=$?
[ "$rc" = 2 ] || fail "check of a malformed profile: exit $rc, want 2"

# raise: up to measured minus the headroom; never down without --lower and a reason.
out=$("$cf" raise --to 90.27)
expect "raise" "87.5% -> 89.77%" "$out"
grep -qx '89.77' "$tmp/coverage-floor.txt" || fail "raise did not write 89.77: $(cat "$tmp/coverage-floor.txt")"
grep -qx '# a comment' "$tmp/coverage-floor.txt" || fail "raise dropped the header"
out=$("$cf" raise "$tmp/unit.out" "$tmp/integration.out")
expect "raise from profiles, below" "the floor only goes up, unchanged" "$out"
grep -qx '89.77' "$tmp/coverage-floor.txt" || fail "raise lowered the floor without --lower"
rc=0; "$cf" raise --to 80 --lower >/dev/null 2>&1 || rc=$?
[ "$rc" = 2 ] || fail "--lower without --reason: exit $rc, want 2"
out=$("$cf" raise --to 80 --lower --reason "deleted the flaky suite")
expect "lower" "LOWERED the floor 89.77% -> 79.50%" "$out"
grep -qx '79.50' "$tmp/coverage-floor.txt" || fail "--lower did not write 79.50"
grep -q '^# lowered .*89.77% -> 79.50%.*deleted the flaky suite$' "$tmp/coverage-floor.txt" || fail "--lower did not record the reason"
expect "same" "unchanged" "$("$cf" raise --to 80)"

echo "test-coverage-floor: ok"
