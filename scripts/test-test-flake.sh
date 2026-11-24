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

# run <want> <exit> <gorun> <line>: test-flake.sh's exit status for a gate
# that printed <line> and exited <exit> (through `go run` when <gorun> is 1).
run() {
  local want=$1 code=$2 gorun=$3 line=$4 rc
  set +e
  (cd "$repo" && env -u GITHUB_ACTIONS FORGE_BIN="$fx/forge" FAKE_EXIT="$code" \
    FAKE_GORUN="$([ "$gorun" = 1 ] && echo 1)" FAKE_LINE="$line" \
    scripts/test-flake.sh --base "$base") >"$fx/out" 2>&1
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

[ "$(cat "$fx/argv")" = "test flakes gate --base $base -- scripts/go-test.sh" ] ||
  fail "test-flake.sh ran forge with '$(cat "$fx/argv")'"

echo "PASS: test-flake.sh"
