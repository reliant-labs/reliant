#!/usr/bin/env bash
# Hermetic test of scripts/air-build.sh: a throwaway module tree and a fake `go`
# on PATH that counts builds, so it needs no toolchain and runs in seconds.
#
# Checks: the first caller builds and later callers reuse; a save that changes
# no bytes reuses; changed bytes in a package file, an embedded file, a file
# added beside one or anywhere under an embedded directory (a new nested
# directory, a directory that held no embedded file yet), a go.mod, or the
# toolchain rebuild, even under an old mtime; a file outside the build's
# inputs does not; a `go list` that fails, prints something unparseable or
# reports package errors beside a successful build records nothing, so every
# caller builds; an import added while a build runs (its old mtime kept), or an
# edit made and reverted while it runs, or a go.mod created in an unlisted
# directory while it runs, leaves that build unrecorded, so the
# next caller builds and lists the new import; N concurrent callers make ONE
# build; a save during a build makes the next caller rebuild; a failed build
# never replaces a delivered binary and fails every caller for the same source
# without building again, until the fail TTL lapses, unless go list reported
# package errors (it may omit inputs), when every caller builds again.
#
# The same file lives in control-plane and reliant; change both together.
set -euo pipefail
SCRIPT="$(cd "$(dirname "$0")" && pwd)/air-build.sh"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
fail=0
FAIL() { echo "FAIL: $*"; fail=1; }

R="$T/repo"
mkdir -p "$R/scripts" "$R/cmd/app" "$R/internal/lib/data/sub" "$R/internal/lib/data/empty" "$R/unrelated" "$T/bin"
cp "$SCRIPT" "$R/scripts/air-build.sh"
echo 'module example.com/app' >"$R/go.mod"
for f in cmd/app/main.go cmd/app/imports internal/lib/lib.go internal/lib/data/sub/x.yaml unrelated/u.go; do : >"$R/$f"; done

# The fake go. `go list` prints the facts the real template prints for this
# tree (the script's awk is what turns them into its inputs): cmd/app imports
# internal/lib, which embeds the pattern "data", plus every directory named in
# cmd/app/imports. FAKE_LIST=fail makes it print part of them and exit 1,
# =garbage print a line in no known form, =incomplete report a package error
# (as -e does), =missing report internal/lib as an error and omit its
# directory and embed (a package go list could not locate). `go build` appends to $T/builds; with FAKE_BUILD_HOLD set it
# then creates $T/building and waits for $T/release; then it runs
# $FAKE_DURING_BUILD (an edit landing mid-build), and fails while
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
    case \${FAKE_LIST:-} in
      fail) echo "D $R/cmd/app"; echo "go: something broke" >&2; exit 1 ;;
      garbage) echo "something unexpected" ;;
      incomplete | missing) echo "X example.com/app/internal/lib" ;;
    esac
    echo "D $R/cmd/app"
    echo "M $R/go.mod"
    if [ "\${FAKE_LIST:-}" != missing ]; then
      echo "D $R/internal/lib"
      echo "P $R/internal/lib|data"
      echo "M $R/go.mod"
    fi
    while IFS= read -r d; do [ -z "\$d" ] || echo "D $R/\$d"; done <"$R/cmd/app/imports"
    [ ! -e "$R/nested/go.mod" ] || echo "M $R/nested/go.mod"
    echo "D $T/modcache/dep@v1"
    echo "M $T/modcache/dep@v1/go.mod" ;;
  build)
    while [ "\$1" != -o ]; do shift; done
    echo build >>"$T/builds"
    if [ -n "\${FAKE_BUILD_HOLD:-}" ]; then
      : >"$T/building"
      i=0
      until [ -e "$T/release" ]; do
        i=\$((i + 1))
        [ \$i -le 400 ] || { echo "fake go: never released" >&2; exit 3; }
        sleep 0.05
      done
    fi
    [ -z "\${FAKE_DURING_BUILD:-}" ] || sh -c "\$FAKE_DURING_BUILD"
    if [ -e "$R/internal/lib/BROKEN" ]; then echo "lib.go:1: syntax error" >&2; exit 1; fi
    # Like go build: a new file renamed over the target, never written in place.
    echo "binary \$(wc -l <"$T/builds" | tr -d ' ')" >"\$2.new" && mv "\$2.new" "\$2" ;;
esac
EOF
chmod +x "$T/bin/go"

# A PATH without Homebrew, so macOS takes /usr/bin/lockf and Linux flock(1).
export PATH="$T/bin:/usr/bin:/bin"
unset AIR_BUILD_LOCKED AIR_BUILD_DIR AIR_BUILD_FAIL_TTL FAKE_GOVERSION FAKE_BUILD_HOLD FAKE_DURING_BUILD FAKE_LIST

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
# A change is new bytes: reuse is decided by content, not mtimes.
e=0
edit() { e=$((e + 1)); printf '// edit %s\n' "$e" >>"$1"; }
# await_building waits (with a deadline) until the fake go is held
# inside a build, so the next step lands mid-build without guessing a sleep.
await_building() {
  local i=0
  until [ -e "$T/building" ]; do
    i=$((i + 1))
    [ $i -le 200 ] || { FAIL "the held build never started"; return 1; }
    sleep 0.05
  done
}
records() { [ -e "$R/tmp/air-build/cmd_app/inputs" ]; }
n=0

n=$((n + 1)); expect "first caller builds" 0 $n tmp/a/app "binary $n"
if ! records; then
  # find here compares whole seconds (BusyBox): the script must never reuse.
  n=$((n + 1)); expect "coarse mtimes: every caller builds" 0 $n tmp/b/app "binary $n"
  echo "skip: the rest needs a find that compares sub-second mtimes"
  exit "$fail"
fi
expect "second caller reuses" 0 $n tmp/b/app "binary $n"
grep -q reused "$T/out" || FAIL "reuse is not reported"
[ ! -e "$R/tmp/b/app.tmp" ] || FAIL "delivery left its .tmp behind"
inputs="$R/tmp/air-build/cmd_app/inputs"
grep -q "/unrelated" "$inputs" && FAIL "inputs hold a directory the build does not read"
grep -q "/modcache" "$inputs" && FAIL "inputs hold the read-only module cache"
grep -qx "r $R/internal/lib/data" "$inputs" || FAIL "the embed pattern's directory is not read whole"

edit "$R/unrelated/u.go"
expect "a file outside the inputs reuses" 0 $n tmp/a/app "binary $n"
touch "$R/internal/lib/lib.go" "$R/internal/lib/data/sub/x.yaml" "$R/go.mod"
expect "a save that changes no bytes reuses" 0 $n tmp/a/app "binary $n"

edit "$R/internal/lib/lib.go"
n=$((n + 1)); expect "a package file rebuilds" 0 $n tmp/a/app "binary $n"
[ "$(cat "$R/tmp/b/app")" = "binary $((n - 1))" ] || FAIL "a rebuild wrote through a binary delivered before it"
expect "then reuses" 0 $n tmp/b/app "binary $n"

edit "$R/internal/lib/data/sub/x.yaml"
n=$((n + 1)); expect "an embedded file rebuilds" 0 $n tmp/a/app "binary $n"
: >"$R/internal/lib/data/sub/new.yaml"
n=$((n + 1)); expect "a file added beside an embedded one rebuilds" 0 $n tmp/a/app "binary $n"
mkdir -p "$R/internal/lib/data/sub/deeper"
: >"$R/internal/lib/data/sub/deeper/z.yaml"
n=$((n + 1)); expect "a file in a new nested embedded directory rebuilds" 0 $n tmp/a/app "binary $n"
: >"$R/internal/lib/data/empty/first.yaml"
n=$((n + 1)); expect "a file in a nested embedded directory that held none rebuilds" 0 $n tmp/a/app "binary $n"
expect "and then reuses" 0 $n tmp/b/app "binary $n"

edit "$R/go.mod"
n=$((n + 1)); expect "go.mod rebuilds" 0 $n tmp/a/app "binary $n"

n=$((n + 1)); FAKE_GOVERSION=go9.9 expect "a new toolchain rebuilds" 0 $n tmp/a/app "binary $n"
FAKE_GOVERSION=go9.9 expect "and reuses under it" 0 $n tmp/b/app "binary $n"
export FAKE_GOVERSION=go9.9

# Content changes that leave mtimes where they were.
touch -r "$R/internal/lib/lib.go" "$T/ref"
echo 'package lib // edited' >"$R/internal/lib/lib.go"
touch -r "$T/ref" "$R/internal/lib/lib.go"
n=$((n + 1)); expect "new bytes under an old mtime rebuild" 0 $n tmp/a/app "binary $n"
expect "and then reuse" 0 $n tmp/b/app "binary $n"
touch -r "$R/internal/lib/data/sub" "$T/ref"
: >"$R/internal/lib/data/sub/added.yaml"
touch -r "$T/ref" "$R/internal/lib/data/sub/added.yaml" "$R/internal/lib/data/sub"
n=$((n + 1)); expect "a file added under a restored directory mtime rebuilds" 0 $n tmp/a/app "binary $n"

# An inventory that is not known complete is never recorded: every caller
# builds while it lasts, and the next good list records again.
for mode in fail garbage incomplete; do
  edit "$R/cmd/app/main.go"
  n=$((n + 1)); FAKE_LIST=$mode expect "go list $mode: the build succeeds" 0 $n tmp/a/app "binary $n"
  records && FAIL "go list $mode: inputs recorded"
  n=$((n + 1)); FAKE_LIST=$mode expect "go list $mode: the next caller builds too" 0 $n tmp/b/app "binary $n"
done
grep -q "something broke" "$T/out" && FAIL "a listing that parsed reported go list's stderr"
FAKE_LIST=fail run tmp/a/app || true
grep -q "go list failed" "$T/out" || FAIL "a failed go list is not reported"
grep -q "something broke" "$T/out" || FAIL "a failed go list's error is not shown"
n=$((n + 1))
n=$((n + 1)); expect "a good list records again" 0 $n tmp/a/app "binary $n"
expect "and reuses" 0 $n tmp/b/app "binary $n"

# The graph moving under a build. An import added mid-build, with the file's
# old mtime put back, so only the before/after inventories can see it: the
# build is delivered but not recorded, and the next caller builds and lists the
# new package, whose own edits then rebuild.
mkdir -p "$R/internal/extra"
: >"$R/internal/extra/e.go"
touch -r "$R/cmd/app/imports" "$T/ref"
edit "$R/cmd/app/main.go"
n=$((n + 1))
FAKE_DURING_BUILD="echo internal/extra >>'$R/cmd/app/imports' && touch -r '$T/ref' '$R/cmd/app/imports'" \
  expect "an import added mid-build: the build is delivered" 0 $n tmp/a/app "binary $n"
records && FAIL "an import added mid-build: the build was recorded reusable"
grep -q "changed while it ran" "$T/out" || FAIL "an unrecorded build does not say why"
n=$((n + 1)); expect "an import added mid-build: the next caller rebuilds" 0 $n tmp/b/app "binary $n"
grep -qx "1 $R/internal/extra" "$inputs" || FAIL "the rebuild did not list the new import"
expect "and then reuses" 0 $n tmp/a/app "binary $n"
edit "$R/internal/extra/e.go"
n=$((n + 1)); expect "an edit to the newly imported package rebuilds" 0 $n tmp/a/app "binary $n"
# An edit made and reverted mid-build: same bytes after as before, but the
# build may have read the edit.
cp "$R/internal/lib/lib.go" "$T/lib.go.orig"
edit "$R/cmd/app/main.go"
n=$((n + 1))
FAKE_DURING_BUILD="echo broken >>'$R/internal/lib/lib.go' && cp '$T/lib.go.orig' '$R/internal/lib/lib.go'" \
  expect "an edit reverted mid-build: the build is delivered" 0 $n tmp/a/app "binary $n"
records && FAIL "an edit reverted mid-build: the build was recorded reusable"
n=$((n + 1)); expect "an edit reverted mid-build: the next caller rebuilds" 0 $n tmp/b/app "binary $n"
expect "and then reuses" 0 $n tmp/a/app "binary $n"
# A go.mod created mid-build in a directory no listed package owns: no listed
# byte changes, so only listing the graph again after the build can see it.
mkdir -p "$R/nested"
edit "$R/cmd/app/main.go"
n=$((n + 1))
FAKE_DURING_BUILD="echo 'module example.com/nested' >'$R/nested/go.mod'" \
  expect "a go.mod created mid-build: the build is delivered" 0 $n tmp/a/app "binary $n"
records && FAIL "a go.mod created mid-build: the build was recorded reusable"
grep -q "package graph changed while it ran" "$T/out" || FAIL "a moved module boundary does not say why"
n=$((n + 1)); expect "a go.mod created mid-build: the next caller rebuilds" 0 $n tmp/b/app "binary $n"
grep -qx "1 $R/nested/go.mod" "$inputs" || FAIL "the rebuild did not list the new go.mod"
expect "and then reuses" 0 $n tmp/a/app "binary $n"

# N concurrent callers for one save: one build, every output delivered.
rm -f "$T/building" "$T/release"
edit "$R/cmd/app/main.go"
for s in c d e; do (FAKE_BUILD_HOLD=1 run "tmp/$s/app") & done
await_building || true
: >"$T/release"
wait
[ "$(builds)" = $((n + 1)) ] || FAIL "3 concurrent callers made $(($(builds) - n)) builds, want 1"
n=$((n + 1))
for s in c d e; do
  [ "$(cat "$R/tmp/$s/app")" = "binary $n" ] || FAIL "concurrent caller $s got '$(cat "$R/tmp/$s/app")'"
done
[ "$fail" = 1 ] || echo "ok: 3 concurrent callers share one build"

# A save while a build runs: the waiting caller must not take that build.
rm -f "$T/building" "$T/release"
edit "$R/cmd/app/main.go"
(FAKE_BUILD_HOLD=1 run tmp/a/app) &
await_building || true
edit "$R/internal/lib/lib.go" # inside the running build
: >"$T/release"
run tmp/b/app || FAIL "caller after a mid-build save failed"
wait
[ "$(builds)" = $((n + 2)) ] || FAIL "a save during a build made $(($(builds) - n)) builds, want 2"
n=$((n + 2))
[ "$(cat "$R/tmp/b/app")" = "binary $n" ] || FAIL "mid-build save: caller got '$(cat "$R/tmp/b/app")', want binary $n"
[ "$fail" = 1 ] || echo "ok: a save during a build makes the next caller rebuild"
rm -f "$T/building" "$T/release"

# Failure: no caller is handed the last good binary as if it were fresh.
: >"$R/internal/lib/BROKEN"
expect "a failed build exits non-zero and keeps the old binary" 1 $((n + 1)) tmp/b/app "binary $n"
grep -q "syntax error" "$T/out" || FAIL "the failed build's error is not shown"
expect "the next caller for the same source fails without building" 1 $((n + 1)) tmp/b/app "binary $n"
grep -q "syntax error" "$T/out" || FAIL "a reused failure does not repeat the error"
for s in c d e; do (rc=0; run "tmp/$s/app" || rc=$?; echo "$rc" >"$T/rc.$s") & done
wait
for s in c d e; do [ "$(cat "$T/rc.$s")" = 1 ] || FAIL "concurrent caller $s exited $(cat "$T/rc.$s") on a failed build"; done
[ "$(builds)" = $((n + 1)) ] || FAIL "callers after a failure built again ($(builds))"
AIR_BUILD_FAIL_TTL=0 expect "a failure older than the TTL is retried" 1 $((n + 2)) tmp/b/app "binary $n"
n=$((n + 2))
# A failure whose list reported package errors is never recorded: that list
# can omit the failing package's inputs, so an edit there (here to a file
# the list left out) must reach the next caller's build, inside the TTL too.
good=$((n - 2))
edit "$R/cmd/app/main.go"
FAKE_LIST=missing expect "a failure on an incomplete list fails" 1 $((n + 1)) tmp/b/app "binary $good"
records && FAIL "a failure on an incomplete list was recorded"
grep -q "package errors" "$T/out" || FAIL "a failure on an incomplete list does not say why it is not recorded"
edit "$R/internal/lib/lib.go"
FAKE_LIST=missing expect "an omitted input edited after it: the next caller builds again" 1 $((n + 2)) tmp/b/app "binary $good"
n=$((n + 2))

rm "$R/internal/lib/BROKEN"
n=$((n + 1)); expect "the fix rebuilds" 0 $n tmp/a/app "binary $n"

rm -f "$inputs"
n=$((n + 1)); expect "missing records rebuild" 0 $n tmp/a/app "binary $n"
expect "and record again" 0 $n tmp/a/app "binary $n"
rm -f "$R/tmp/air-build/cmd_app/digest"
n=$((n + 1)); expect "a missing digest rebuilds" 0 $n tmp/a/app "binary $n"

exit "$fail"
