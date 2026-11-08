#!/usr/bin/env bash
# Hermetic test of scripts/air-build.sh: a throwaway module tree and a fake `go`
# on PATH that counts builds, so it needs no toolchain and runs in a second.
#
# Checks: the first caller builds and later callers reuse; any change to a
# package file, an embedded file (or a file added beside one), a go.mod, or the
# toolchain rebuilds; a file outside the build's inputs does not; N concurrent
# callers make ONE build; a save during a build makes the next caller rebuild
# and leaves that build with no recorded inputs;
# a failed build never replaces a delivered binary and fails every caller for
# the same source without building again, until the fail TTL lapses.
set -euo pipefail
SCRIPT="$(cd "$(dirname "$0")" && pwd)/air-build.sh"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
fail=0
FAIL() { echo "FAIL: $*"; fail=1; }

R="$T/repo"
mkdir -p "$R/scripts" "$R/cmd/app" "$R/internal/lib/data/sub" "$R/unrelated" "$T/bin"
cp "$SCRIPT" "$R/scripts/air-build.sh"
echo 'module example.com/app' >"$R/go.mod"
: >"$R/cmd/app/main.go" >"$R/internal/lib/lib.go" >"$R/internal/lib/data/sub/x.yaml" >"$R/unrelated/u.go"

# The fake go. `go list` prints the facts the real template prints for this
# tree (the script's awk is what turns them into its inputs); `go build`
# appends to $T/builds, takes FAKE_BUILD_SLEEP seconds, and fails while
# internal/lib/BROKEN exists.
cat >"$T/bin/go" <<EOF
#!/bin/sh
case \$1 in
  env)
    shift
    for v in "\$@"; do
      case \$v in
        GOVERSION) echo "\${FAKE_GOVERSION:-go1.0}" ;;
        GOMODCACHE) echo "$T/modcache" ;;
        GOROOT) echo "$T/goroot" ;;
        *) echo ;;
      esac
    done ;;
  list)
    echo "D $R/cmd/app"
    echo "M $R/go.mod"
    echo "D $R/internal/lib"
    echo "E $R/internal/lib|data/sub/x.yaml"
    echo "M $R/go.mod"
    echo "D $T/modcache/dep@v1"
    echo "M $T/modcache/dep@v1/go.mod" ;;
  build)
    while [ "\$1" != -o ]; do shift; done
    echo build >>"$T/builds"
    sleep "\${FAKE_BUILD_SLEEP:-0}"
    if [ -e "$R/internal/lib/BROKEN" ]; then echo "lib.go:1: syntax error" >&2; exit 1; fi
    # Like go build: a new file renamed over the target, never written in place.
    echo "binary \$(wc -l <"$T/builds" | tr -d ' ')" >"\$2.new" && mv "\$2.new" "\$2" ;;
esac
EOF
chmod +x "$T/bin/go"

# A PATH without Homebrew, so macOS takes /usr/bin/lockf and Linux flock(1).
export PATH="$T/bin:/usr/bin:/bin"
unset AIR_BUILD_LOCKED AIR_BUILD_DIR AIR_BUILD_FAIL_TTL FAKE_GOVERSION FAKE_BUILD_SLEEP

builds() { [ -f "$T/builds" ] && wc -l <"$T/builds" | tr -d ' ' || echo 0; }
run() { (cd "$R" && sh scripts/air-build.sh ./cmd/app "$1") >"$T/out" 2>&1; }
# expect <label> <want exit> <want total builds> <output> <want output content>
expect() {
  local label=$1 code=$2 want=$3 out=$4 content=$5 rc=0
  run "$out" || rc=$?
  [ "$rc" = "$code" ] || { FAIL "$label: exit $rc, want $code"; cat "$T/out"; }
  [ "$(builds)" = "$want" ] || FAIL "$label: $(builds) builds, want $want"
  [ "$(cat "$R/$out" 2>/dev/null)" = "$content" ] || FAIL "$label: $out holds '$(cat "$R/$out" 2>/dev/null)', want '$content'"
  [ "$fail" = 1 ] || echo "ok: $label"
}
# A change is a plain touch: air-build.sh waits a clock tick after stamping a
# build's start, so a touch after a call is always newer than its stamp.

expect "first caller builds" 0 1 tmp/a/app "binary 1"
if [ ! -e "$R/tmp/air-build/cmd_app/inputs" ]; then
  # find here compares whole seconds (BusyBox): the script must never reuse.
  expect "coarse mtimes: every caller builds" 0 2 tmp/b/app "binary 2"
  echo "skip: the rest needs a find that compares sub-second mtimes"
  exit "$fail"
fi
expect "second caller reuses" 0 1 tmp/b/app "binary 1"
grep -q reused "$T/out" || FAIL "reuse is not reported"
[ ! -e "$R/tmp/b/app.tmp" ] || FAIL "delivery left its .tmp behind"
grep -q "/unrelated" "$R/tmp/air-build/cmd_app/inputs" && FAIL "inputs hold a directory the build does not read"
grep -q "/modcache" "$R/tmp/air-build/cmd_app/inputs" && FAIL "inputs hold the read-only module cache"

touch "$R/unrelated/u.go"
expect "a file outside the inputs reuses" 0 1 tmp/a/app "binary 1"

touch "$R/internal/lib/lib.go"
expect "a package file rebuilds" 0 2 tmp/a/app "binary 2"
[ "$(cat "$R/tmp/b/app")" = "binary 1" ] || FAIL "a rebuild wrote through a binary delivered before it"
expect "then reuses" 0 2 tmp/b/app "binary 2"

touch "$R/internal/lib/data/sub/x.yaml"
expect "an embedded file rebuilds" 0 3 tmp/a/app "binary 3"

: >"$R/internal/lib/data/sub/new.yaml"
expect "a file added beside an embedded one rebuilds" 0 4 tmp/a/app "binary 4"

touch "$R/go.mod"
expect "go.mod rebuilds" 0 5 tmp/a/app "binary 5"

FAKE_GOVERSION=go9.9 expect "a new toolchain rebuilds" 0 6 tmp/a/app "binary 6"
FAKE_GOVERSION=go9.9 expect "and reuses under it" 0 6 tmp/b/app "binary 6"
export FAKE_GOVERSION=go9.9

# N concurrent callers for one save: one build, every output delivered.
touch "$R/cmd/app/main.go"
for s in c d e; do (FAKE_BUILD_SLEEP=1 run "tmp/$s/app") & done
wait
[ "$(builds)" = 7 ] || FAIL "3 concurrent callers made $(($(builds) - 6)) builds, want 1"
for s in c d e; do
  [ "$(cat "$R/tmp/$s/app")" = "binary 7" ] || FAIL "concurrent caller $s got '$(cat "$R/tmp/$s/app")'"
done
[ "$fail" = 1 ] || echo "ok: 3 concurrent callers share one build"

# await_builds <n>: wait (up to 10s) until the fake go has begun build n.
await_builds() {
  local i=0
  while [ "$(builds)" -lt "$1" ]; do
    i=$((i + 1))
    [ "$i" -le 1000 ] || { FAIL "build $1 never started"; return 1; }
    sleep 0.01
  done
}

# A save while a build runs: the waiting caller must not take that build.
touch "$R/cmd/app/main.go"
(FAKE_BUILD_SLEEP=1 run tmp/a/app) &
await_builds 8
: >"$R/internal/lib/lib.go" # now: after the running build began
run tmp/b/app || FAIL "caller after a mid-build save failed"
wait
[ "$(builds)" = 9 ] || FAIL "a save during a build made $(($(builds) - 7)) builds, want 2"
[ "$(cat "$R/tmp/b/app")" = "binary 9" ] || FAIL "mid-build save: caller got '$(cat "$R/tmp/b/app")', want binary 9"
[ "$fail" = 1 ] || echo "ok: a save during a build makes the next caller rebuild"

# A build whose inputs changed while it ran records none, so its inputs list
# (enumerated beside the build) is never trusted for reuse.
touch "$R/cmd/app/main.go"
(FAKE_BUILD_SLEEP=1 run tmp/a/app) &
await_builds 10
: >"$R/internal/lib/lib.go"
wait
[ ! -e "$R/tmp/air-build/cmd_app/inputs" ] || FAIL "a build whose inputs changed mid-build recorded them"
expect "the next caller after a mid-build change rebuilds" 0 11 tmp/b/app "binary 11"
expect "and then reuses" 0 11 tmp/c/app "binary 11"

# Failure: no caller is handed the last good binary as if it were fresh.
: >"$R/internal/lib/BROKEN"
expect "a failed build exits non-zero and keeps the old binary" 1 12 tmp/b/app "binary 11"
grep -q "syntax error" "$T/out" || FAIL "the failed build's error is not shown"
expect "the next caller for the same source fails without building" 1 12 tmp/b/app "binary 11"
grep -q "syntax error" "$T/out" || FAIL "a reused failure does not repeat the error"
for s in c d e; do (rc=0; run "tmp/$s/app" || rc=$?; echo "$rc" >"$T/rc.$s") & done
wait
for s in c d e; do [ "$(cat "$T/rc.$s")" = 1 ] || FAIL "concurrent caller $s exited $(cat "$T/rc.$s") on a failed build"; done
[ "$(builds)" = 12 ] || FAIL "callers after a failure built again ($(builds))"
AIR_BUILD_FAIL_TTL=0 expect "a failure older than the TTL is retried" 1 13 tmp/b/app "binary 11"

rm "$R/internal/lib/BROKEN"
expect "the fix rebuilds" 0 14 tmp/a/app "binary 14"

rm -rf "$R/tmp/air-build/cmd_app/inputs"
expect "missing records rebuild" 0 15 tmp/a/app "binary 15"
expect "and record again" 0 15 tmp/a/app "binary 15"

exit "$fail"
