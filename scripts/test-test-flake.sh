#!/usr/bin/env bash
# test-test-flake.sh — hermetic self-test of scripts/test-flake.sh's reading
# of `forge test flakes gate`: no network, no real forge, no Go build.
#
# A fake forge (FORGE_BIN, through the real scripts/forge.sh) prints one of
# the gate's lines and exits with a chosen status, in a throwaway git repo.
# The gate's exit 2 means three things, told apart by its line, and a wrapper
# can turn any non-zero exit into 1 (`go run` did: it prints `exit status 2`
# and exits 1), so each line is checked at both statuses:
#
#   no new or edited tests   -> 0 (reliant#3243's merge-queue regression read it as 1)
#   could not run            -> 1
#   could not identify       -> 2
#   FAILED, or no known line -> 1
#   a pass                   -> 0
#
#   scripts/test-test-flake.sh        (seconds; needs bash, git, coreutils)
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
fx=$(mktemp -d "${TMPDIR:-/tmp}/test-test-flake.XXXXXX")
trap 'rm -rf "$fx"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

repo=$fx/repo
mkdir -p "$repo/scripts"
cp "$here/test-flake.sh" "$here/forge.sh" "$repo/scripts/"
printf 'forge_version: v1.2.3\n' >"$repo/forge.yaml"
printf 'module github.com/x/y\n\ngo 1.25\n' >"$repo/go.mod"
git -C "$repo" init -q
git -C "$repo" -c user.email=t@example.com -c user.name=t add -A
git -C "$repo" -c user.email=t@example.com -c user.name=t commit -q -m fixture
base=$(git -C "$repo" rev-parse HEAD)

# The fake forge: logs its argv, prints $FAKE_LINE (if any) and, when
# FAKE_GORUN is set, `exit status <FAKE_EXIT>` and exits 1 the way `go run`
# reported a non-zero exit; otherwise exits FAKE_EXIT.
cat >"$fx/forge" <<EOF
#!/usr/bin/env bash
echo "\$*" >"$fx/argv"
[ -z "\${FAKE_LINE:-}" ] || echo "\$FAKE_LINE"
if [ -n "\${FAKE_GORUN:-}" ] && [ "\${FAKE_EXIT:-0}" -ne 0 ]; then echo "exit status \$FAKE_EXIT" >&2; exit 1; fi
exit "\${FAKE_EXIT:-0}"
EOF
chmod +x "$fx/forge"

# The per-package re-judge's seams: tools/test-affected's changed tests, and a
# go test that logs each package it runs and fails the one in $FAKE_GT_FAIL.
cat >"$fx/changed" <<'EOF2'
#!/usr/bin/env bash
printf './internal/db\t^(TestA|TestB)$\n./internal/chat\t^(TestC)$\n./sdk/x\t^(TestS)$\tsdk/go\n'
EOF2
cat >"$fx/gotest" <<EOF2
#!/usr/bin/env bash
pkg="\${!#}"; echo "\$*" >>"$fx/gt"
[ "\$pkg" != "\${FAKE_GT_FAIL:-none}" ]
EOF2
chmod +x "$fx/changed" "$fx/gotest"

# run <want> <exit> <gorun> <line>: test-flake.sh's exit status for a gate
# that printed <line> and exited <exit> (through `go run` when <gorun> is 1).
run() {
  local want=$1 code=$2 gorun=$3 line=$4 rc
  set +e
  : >"$fx/gt"
  (cd "$repo" && env -u GITHUB_ACTIONS FORGE_BIN="$fx/forge" FAKE_EXIT="$code" \
    TEST_FLAKE_CHANGED_CMD="$fx/changed" TEST_FLAKE_GO_TEST="$fx/gotest" \
    FAKE_GORUN="$([ "$gorun" = 1 ] && echo 1)" FAKE_LINE="$line" \
    FAKE_GT_FAIL="${FAKE_GT_FAIL:-}" scripts/test-flake.sh --base "$base") >"$fx/out" 2>&1
  rc=$?
  set -e
  [ "$rc" = "$want" ] || fail "gate exit $code (go run: $gorun) printing '${line:-<nothing>}': test-flake.sh exited $rc, want $want
$(cat "$fx/out")"
}

none="flake gate: no new or edited tests in ${base:0:9}..HEAD"
for gorun in 0 1; do
  run 0 2 "$gorun" "$none"
  run 1 2 "$gorun" "flake gate: could not run scripts/go-test.sh: permission denied"
  run 2 2 "$gorun" "flake gate: could not identify the changed tests in $base..HEAD: bad revision"
  run 1 1 "$gorun" "flake gate: FAILED after 3s; reproduce with: go test -run '^TestX\$' ./x"
  run 1 2 "$gorun" ""
done
run 0 0 0 "flake gate: selected tests passed 3/3 in 2s (1 packages)"
run 1 3 0 ""

# Out of budget only: the timed-out packages' changed tests, one package each.
to=$(printf 'panic: test timed out after 10m0s\n\trunning tests:\n\t\tTestIsRetryableError (1m)\nFAIL\tgithub.com/x/y/internal/db\t600.094s\nok\tgithub.com/x/y/internal/chat\t3s\nflake gate: FAILED after 11m16s; reproduce with: scripts/go-test.sh -short -count=3 -timeout 10m -run %s ./internal/db ./internal/chat' "'^(TestA|TestB|TestC)\$'")
run 0 1 0 "$to"
grep -q -- '-run ^(TestA|TestB)$ ./internal/db' "$fx/gt" || fail "a timed-out package is judged alone: $(cat "$fx/gt")"
! grep -q 'internal/chat' "$fx/gt" || fail "a package that passed is not run again"
FAKE_GT_FAIL=./internal/db run 1 1 0 "$to"
run 1 1 0 "$(printf '%s\n--- FAIL: TestA (1s)' "$to")"    # a real failure beside the timeout fails
run 1 1 0 "$(printf '%s\nFAIL\tgithub.com/x/y/internal/db [build failed]' "$to")"
# Every test of the packages (the selection outgrew -run): every changed test, one package each.
all="flake gate: FAILED after 9m; reproduce with: scripts/go-test.sh -short -count=3 -timeout 10m -run '^(Test|Fuzz)' ./internal/db ./internal/chat"
run 0 1 0 "$(printf -- '--- FAIL: TestOld (0.1s)\n%s' "$all")"
grep -q 'internal/db' "$fx/gt" && grep -q 'internal/chat' "$fx/gt" || fail "the fallback judges every package's changed tests: $(cat "$fx/gt")"
! grep -q 'sdk/x' "$fx/gt" || fail "a nested module's tests are not run from the root"
FAKE_GT_FAIL=./internal/chat run 1 1 0 "$all"

[ "$(cat "$fx/argv")" = "test flakes gate --base $base -- scripts/go-test.sh" ] ||
  fail "test-flake.sh ran forge with '$(cat "$fx/argv")'"

echo "PASS: test-flake.sh"
