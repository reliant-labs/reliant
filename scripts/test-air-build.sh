#!/usr/bin/env bash
# Hermetic test of scripts/air-build.sh: a throwaway tree and a fake `go` on
# PATH that records every build, so it needs no toolchain and runs in seconds.
#
# The fake models what the script relies on go build for: it relinks only when
# the source changed since its last successful link (otherwise it leaves the
# target as it is), writes the target only on success and then as a new file,
# and fails while BROKEN exists. It also detects two builds running at once.
#
# Checks: every caller runs go build and gets the shared binary, hard-linked;
# a caller after no change gets the same file without a relink; a change
# relinks without writing through binaries delivered earlier; three concurrent
# callers are serialized by the lock (two provably wait on it while the first
# builds), make one link between them, and all get that one file; a failed
# build exits non-zero and leaves the shared binary and every delivered copy
# intact, and the next caller tries again; a lock holder killed mid-build does
# not wedge the next caller. No step waits a fixed time: each polls for the
# event it needs, with a deadline.
#
# The same file lives in control-plane and reliant; change both together.
set -euo pipefail
SCRIPT="$(cd "$(dirname "$0")" && pwd)/air-build.sh"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
fail=0
FAIL() { echo "FAIL: $*"; fail=1; }

R="$T/repo"
mkdir -p "$R/scripts" "$R/cmd/app" "$T/bin"
cp "$SCRIPT" "$R/scripts/air-build.sh"
echo 'package main // v0' >"$R/cmd/app/main.go"

cat >"$T/bin/go" <<EOF
#!/bin/sh
[ "\$1" = build ] || { echo "fake go: unexpected \$*" >&2; exit 2; }
while [ "\$1" != -o ]; do shift; done
target=\$2
mkdir "$T/inbuild" 2>/dev/null || echo "build \$\$ began while another ran" >>"$T/overlap"
echo "start \$\$" >>"$T/log"
i=0
while [ -e "$T/hold" ]; do
  i=\$((i + 1))
  [ \$i -le 400 ] || { echo "fake go: held too long" >&2; rmdir "$T/inbuild"; exit 3; }
  sleep 0.05
done
rc=0
if [ -e "$R/cmd/app/BROKEN" ]; then
  echo "main.go:1: syntax error" >&2; rc=1
elif [ -e "\$target" ] && cmp -s "$R/cmd/app/main.go" "$T/linked.src"; then
  echo "noop \$\$" >>"$T/log" # up to date: go build leaves the target be
else
  n=\$(grep -c '^link' "$T/log" || true); n=\$((n + 1))
  echo "link \$\$" >>"$T/log"
  echo "binary \$n" >"\$target.new" && mv "\$target.new" "\$target" # a new file, renamed over
  cp "$R/cmd/app/main.go" "$T/linked.src"
fi
echo "end \$\$" >>"$T/log"
rmdir "$T/inbuild"
exit \$rc
EOF
chmod +x "$T/bin/go"

# A PATH without Homebrew, so macOS takes /usr/bin/lockf and Linux flock(1).
export PATH="$T/bin:/usr/bin:/bin"
export AIR_BUILD_DIR="$T/air"
unset AIR_BUILD_LOCKED
shared="$T/air/cmd_app/bin"
lockpath="$T/air/cmd_app/lock"

count() { grep -c "^$1" "$T/log" 2>/dev/null || true; }
inode() { ls -i "$1" 2>/dev/null | awk '{print $1}'; }
run() { (cd "$R" && sh scripts/air-build.sh ./cmd/app "$1") >"$T/out" 2>&1; }
# run_bg <name> <output>: run a caller in the background; $T/rc.<name> holds
# its exit status once it is done.
run_bg() {
  rm -f "$T/rc.$1"
  (rc=0; (cd "$R" && sh scripts/air-build.sh ./cmd/app "$2") >"$T/out.$1" 2>&1 || rc=$?; echo "$rc" >"$T/rc.$1") &
}
# await <what> <command...>: poll until the command succeeds, for 20s at most.
await() {
  local what=$1 i=0
  shift
  until "$@"; do
    i=$((i + 1))
    [ $i -le 400 ] || { FAIL "timed out waiting for $what"; return 1; }
    sleep 0.05
  done
}
# lock_procs: processes whose command line names this test's lock file (the
# flock/lockf holder, and every caller still waiting for the lock).
lock_procs() {
  { ps -A -o args= 2>/dev/null || ps -o args; } | LP="$lockpath" awk 'index($0, ENVIRON["LP"])' | wc -l | tr -d ' '
}
# blocked_on_lock <n>: at least n more processes name the lock than $base did.
blocked_on_lock() { [ "$(lock_procs)" -ge $((base + $1)) ]; }
# tree <pid>: the pid and all its descendants.
tree() {
  { ps -A -o pid= -o ppid= 2>/dev/null || ps -o pid,ppid; } | awk -v root="$1" '
    { parent[$1] = $2 }
    END {
      for (p in parent) {
        q = p
        while (q != root && (q in parent) && parent[q] != q) q = parent[q]
        if (q == root) print p
      }
    }'
}
ok() { [ "$fail" = 1 ] || echo "ok: $*"; }

# expect <label> <want exit> <want links> <want go runs> <output> <want content>
expect() {
  local label=$1 code=$2 links=$3 runs=$4 out=$5 content=$6 rc=0
  run "$out" || rc=$?
  [ "$rc" = "$code" ] || { FAIL "$label: exit $rc, want $code"; cat "$T/out"; }
  [ "$(count link)" = "$links" ] || FAIL "$label: $(count link) links, want $links"
  [ "$(count start)" = "$runs" ] || FAIL "$label: go build ran $(count start) times, want $runs"
  [ "$(cat "$R/$out" 2>/dev/null)" = "$content" ] || FAIL "$label: $out holds '$(cat "$R/$out" 2>/dev/null)', want '$content'"
  ok "$label"
}

expect "first caller builds" 0 1 1 tmp/a/app "binary 1"
[ "$(inode "$R/tmp/a/app")" = "$(inode "$shared")" ] || FAIL "the output is not a hard link to the shared binary"
[ ! -e "$R/tmp/a/app.tmp" ] || FAIL "delivery left its .tmp behind"
expect "a second caller runs go build, which relinks nothing" 0 1 2 tmp/b/app "binary 1"
[ "$(inode "$R/tmp/b/app")" = "$(inode "$R/tmp/a/app")" ] || FAIL "two callers for one source got different files"
expect "a caller whose output already is the shared binary" 0 1 3 tmp/a/app "binary 1"

echo 'package main // v1' >"$R/cmd/app/main.go"
expect "a change relinks" 0 2 4 tmp/a/app "binary 2"
[ "$(cat "$R/tmp/b/app")" = "binary 1" ] || FAIL "a relink wrote through a binary delivered before it"

# Three concurrent callers for one save. The first build is held until the
# other two are provably blocked on the lock, so only the lock can keep their
# builds from starting.
echo 'package main // v2' >"$R/cmd/app/main.go"
: >"$T/hold"
run_bg c tmp/c/app
await "the first build to start" test -d "$T/inbuild"
base=$(lock_procs)
run_bg d tmp/d/app
run_bg e tmp/e/app
await "two more callers to block on the lock" blocked_on_lock 2
[ "$(count start)" = 5 ] || FAIL "a second build started while the first held the lock"
rm -f "$T/hold"
await "the three callers to finish" test -e "$T/rc.c" -a -e "$T/rc.d" -a -e "$T/rc.e"
[ ! -e "$T/overlap" ] || { FAIL "builds overlapped"; cat "$T/overlap"; }
[ "$(count start)" = 7 ] || FAIL "3 concurrent callers ran go build $(($(count start) - 4)) times, want 3"
[ "$(count link)" = 3 ] || FAIL "3 concurrent callers linked $(($(count link) - 2)) times, want 1"
for s in c d e; do
  [ "$(cat "$T/rc.$s")" = 0 ] || { FAIL "concurrent caller $s exited $(cat "$T/rc.$s")"; cat "$T/out.$s"; }
  [ "$(inode "$R/tmp/$s/app")" = "$(inode "$shared")" ] || FAIL "concurrent caller $s did not get the shared binary"
done
[ "$(cat "$shared")" = "binary 3" ] || FAIL "the shared binary holds '$(cat "$shared")', want binary 3"
ok "3 concurrent callers are serialized, link once and share one file"

# Failure: the shared binary and every delivered copy stay as they were.
before=$(inode "$shared")
: >"$R/cmd/app/BROKEN"
expect "a failed build exits non-zero" 1 3 8 tmp/a/app "binary 2"
grep -q "syntax error" "$T/out" || FAIL "the failed build's error is not shown"
[ "$(inode "$shared")" = "$before" ] && [ "$(cat "$shared")" = "binary 3" ] || FAIL "a failed build touched the shared binary"
[ "$(cat "$R/tmp/c/app")" = "binary 3" ] || FAIL "a failed build touched a delivered binary"
expect "the next caller tries again" 1 3 9 tmp/a/app "binary 2"
rm "$R/cmd/app/BROKEN"
expect "and once fixed, the shared binary is delivered" 0 3 10 tmp/a/app "binary 3"

# A lock holder killed mid-build: the kernel releases the lock, the shared
# binary is untouched, and the next caller builds.
echo 'package main // v3' >"$R/cmd/app/main.go"
: >"$T/hold"
run_bg f tmp/f/app
victim=$!
await "the doomed build to start" test -d "$T/inbuild"
# Kill the caller's whole tree at once (the lock tool, the script, go), the
# way a dying Air or a Ctrl-C takes it down.
# shellcheck disable=SC2046 # one pid per word
kill -KILL $(tree "$victim") 2>/dev/null || FAIL "could not kill the lock holder"
wait "$victim" 2>/dev/null || true
rm -f "$T/hold"
rmdir "$T/inbuild" 2>/dev/null || true # the fake died before cleaning up
[ "$(cat "$shared")" = "binary 3" ] || FAIL "a killed build touched the shared binary"
run_bg g tmp/g/app
await "a caller after a killed lock holder to finish" test -e "$T/rc.g"
[ "$(cat "$T/rc.g" 2>/dev/null)" = 0 ] || { FAIL "the caller after a killed holder exited $(cat "$T/rc.g" 2>/dev/null)"; cat "$T/out.g"; }
[ "$(cat "$R/tmp/g/app" 2>/dev/null)" = "binary 4" ] || FAIL "the caller after a killed holder got '$(cat "$R/tmp/g/app" 2>/dev/null)'"
ok "a lock holder killed mid-build does not wedge the next caller"

rc=0
(cd "$R" && sh scripts/air-build.sh) >/dev/null 2>&1 || rc=$?
[ "$rc" = 2 ] || FAIL "no arguments: exit $rc, want 2"
ok "usage error exits 2"

exit "$fail"
