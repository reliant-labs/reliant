#!/bin/sh
# air-build.sh — build one Go main package ONCE per save, however many Air
# watchers ask for it, and hand each watcher its own copy of the binary.
#
#   usage: scripts/air-build.sh <main package> <output path>
#   e.g.   scripts/air-build.sh ./cmd/control-plane ./tmp/admin-server/control-plane
#
# WHY THIS EXISTS
# ---------------
# Several dev processes are the SAME binary run under different subcommands,
# each with its own Air watcher (separate processes, ports, logs and readiness
# in forge's workload model). Every save used to make each watcher run an
# identical `go build` at the same moment: N concurrent compiles and N links of
# one binary. Go's build cache does not dedupe work that is in flight in
# another process, so the N builds raced each other for the CPU instead.
#
# WHAT THIS DOES
# --------------
# Under an exclusive lock, the first caller builds into a shared path
# (tmp/air-build/<package>/bin) and records:
#   started  — a file whose mtime is the moment that build BEGAN
#   inputs   — every repo-local file and directory the build read, as `go list
#              -deps` reports it: each non-standard package's directory, its
#              embedded files (and their directories), and the go.mod/go.sum of
#              every module that is not in the read-only module cache. So a
#              go.mod `replace`, a go.work member and a //go:embed are all
#              covered, and no hand-kept exclusion list can drift.
#   key      — the toolchain and flags (go version, GOOS/GOARCH, GOFLAGS, …)
#   state    — ok, or fail plus the build's output
# Every later caller takes the lock, and if no recorded input is newer than
# `started` and the key matches, reuses that build instead of starting another.
# Because `started` is stamped BEFORE `go build` reads anything, a file saved
# while a build runs is newer than `started`, so the next caller rebuilds.
#
# A failed build never replaces a published binary: it writes `state=fail`, so
# a caller arriving for the same save prints the same error and exits non-zero
# rather than taking the last good binary as if it were fresh (Air then keeps
# the old binary, exactly as before). A failure is reused only for
# AIR_BUILD_FAIL_TTL seconds, so a transient one (a killed build, a full disk)
# is retried by the next caller rather than remembered.
#
# A save that changes no compiled output (an unchanged file re-saved, a comment
# appended) costs no link at all: go build finds the published binary already
# up to date and leaves it be.
#
# Builds use -trimpath: compiled packages are then keyed by module path, not by
# this checkout's absolute path, so worktrees at the same commit share them in
# the one GOCACHE. (Stack traces name files by module path, too.)
#
# THE LOCK
# --------
# A kernel lock released when its holder dies, so a killed build never wedges
# the next save: flock(1) (util-linux, and BusyBox in the alpine dev image),
# else /usr/bin/lockf (every macOS; Homebrew's flock is not guaranteed there),
# else perl's flock. A mkdir lock would need stale-pid recovery, which cannot be
# made race-free. POSIX sh, so it also runs in the alpine dev image, which has
# no bash.
set -eu

if [ $# -ne 2 ]; then
  echo "usage: $0 <main package> <output path>" >&2
  exit 2
fi
pkg=$1
out=$2

dir=${AIR_BUILD_DIR:-tmp/air-build}/$(printf '%s' "${pkg#./}" | tr -c 'A-Za-z0-9._-' '_')

if [ -z "${AIR_BUILD_LOCKED:-}" ]; then
  mkdir -p "$dir"
  AIR_BUILD_LOCKED=1
  export AIR_BUILD_LOCKED
  if command -v flock >/dev/null 2>&1; then
    exec flock "$dir/lock" /bin/sh "$0" "$@"
  elif [ -x /usr/bin/lockf ]; then
    exec /usr/bin/lockf -k "$dir/lock" /bin/sh "$0" "$@"
  elif command -v perl >/dev/null 2>&1; then
    # $^F high so the locked descriptor survives exec into the build.
    exec perl -e 'BEGIN { $^F = 255 } use Fcntl ":flock";
      open(my $f, ">>", shift) or die "air-build: open lock: $!\n";
      flock($f, LOCK_EX) or die "air-build: flock: $!\n";
      exec @ARGV or die "air-build: exec: $!\n";' "$dir/lock" /bin/sh "$0" "$@"
  fi
  echo "air-build: need flock, /usr/bin/lockf or perl to serialize builds" >&2
  exit 2
fi

# ── Holding the lock from here on. ──────────────────────────────────────────

flags="-trimpath -buildvcs=false"
key=$(printf '%s\n' "air-build v1" "$pkg" "CGO_ENABLED=0" "$flags" \
  "$(go env GOVERSION GOOS GOARCH GOFLAGS GOWORK GOEXPERIMENT GOARM64 GOAMD64)")

# deliver hard-links (or, across filesystems, copies) the published binary to
# the caller's output path through a rename, so a running process is never
# handed a half-written file.
deliver() {
  # Already this build (a hard link): GNU mv refuses to move a file onto
  # another link of itself.
  [ "$dir/bin" -ef "$out" ] && return 0
  mkdir -p "$(dirname "$out")"
  rm -f "$out.tmp"
  ln "$dir/bin" "$out.tmp" 2>/dev/null || cp "$dir/bin" "$out.tmp"
  mv -f "$out.tmp" "$out"
}

# unchanged succeeds when the last build attempt saw every input as it is now
# and ran with the same toolchain and flags.
unchanged() {
  [ -f "$dir/started" ] && [ -s "$dir/inputs" ] && [ -f "$dir/state" ] && [ -f "$dir/key" ] || return 1
  [ "$(cat "$dir/key")" = "$key" ] || return 1
  # Depth 0: a recorded file, or a directory whose entries were added, removed
  # or renamed. Depth 1: a file directly inside a recorded directory (its
  # subdirectories are other packages, recorded on their own when imported).
  # A recorded path that no longer exists makes find fail: not unchanged.
  newer=$(tr '\n' '\0' <"$dir/inputs" | xargs -0 sh -c '
    s=$1; shift
    find "$@" -maxdepth 0 -newer "$s" -print &&
      find "$@" -maxdepth 1 ! -type d -newer "$s" -print' sh "$dir/started") || return 1
  [ -z "$newer" ]
}

if unchanged; then
  state=$(head -n 1 "$dir/state")
  case $state in
    ok)
      if [ -f "$dir/bin" ]; then
        deliver
        echo "air-build: $pkg unchanged since its last build; reused it"
        exit 0
      fi
      ;;
    fail\ *)
      failed_at=${state#fail }
      if [ $(($(date +%s) - failed_at)) -lt "${AIR_BUILD_FAIL_TTL:-60}" ]; then
        tail -n +2 "$dir/state" >&2
        echo "air-build: $pkg failed to build for this same source (above); not rebuilding" >&2
        exit 1
      fi
      ;;
  esac
fi

# ── Build. ──────────────────────────────────────────────────────────────────
# A crash from here on leaves no state, so the next caller rebuilds.
rm -f "$dir/state" "$dir/started.new" "$dir/inputs.new"
: >"$dir/started.new"
# Linux stamps files from a coarse clock (one tick, up to 10ms), so a save in
# the same tick as this stamp has an equal mtime and does not count as newer.
# Waiting out a tick before go build reads anything puts such a save before
# the build, where it belongs.
sleep 0.02
# Reuse rests on find telling apart two mtimes 20ms apart. BusyBox find
# compares whole seconds and some filesystems store no more, so a save in the
# same second as a build start would look old: there, every caller builds, as
# before this script.
: >"$dir/probe"
precise=$(find "$dir/probe" -newer "$dir/started.new" -print)

# Record what the build reads, alongside the build itself. -e: a tree that
# does not compile still lists its packages, so a failure is recorded against
# the inputs that caused it.
gomodcache=$(go env GOMODCACHE)
goroot=$(go env GOROOT)
(
  # One line per fact: "D <package dir>", "E <package dir>|<embedded file,
  # relative>", "M <go.mod>". awk expands an embedded file to itself plus every
  # directory between it and its package (so a file added anywhere under an
  # embedded directory is seen), and a go.mod to its go.sum and go.work
  # siblings. Paths in the read-only module cache and GOROOT never change.
  # shellcheck disable=SC2016 # a go template, not a shell expansion
  CGO_ENABLED=0 go list -e -deps -f '{{if not .Standard}}D {{.Dir}}
{{range .EmbedFiles}}E {{$.Dir}}|{{.}}
{{end}}{{with .Module}}{{with .GoMod}}M {{.}}
{{end}}{{end}}{{end}}' "$pkg" 2>/dev/null | awk -v cache="$gomodcache/" -v root="$goroot/" '
    { kind = substr($0, 1, 1); p = substr($0, 3) }
    kind == "E" {
      bar = index(p, "|"); pdir = substr(p, 1, bar - 1); rel = substr(p, bar + 1)
      p = pdir "/" rel
      n = split(rel, parts, "/"); d = pdir
      for (i = 1; i < n; i++) { d = d "/" parts[i]; out(d) }
    }
    { out(p) }
    kind == "M" { sub(/\.mod$/, "", p); out(p ".sum"); out(p ".work"); out(p ".work.sum") }
    function out(path) {
      if (path == "" || index(path, cache) == 1 || index(path, root) == 1) return
      print path
    }' | sort -u | while IFS= read -r p; do
    [ -e "$p" ] && printf '%s\n' "$p" # drop a go.sum or go.work that does not exist
  done >"$dir/inputs.new"
) &
list_pid=$!

# Straight onto the published path: go build links in its own work directory
# and replaces the target only on success, with a new file (remove, then
# rename), so a failed build leaves it alone and a running process, a hard link
# to the old file, is never touched. And when the target already IS this
# build (a save that changed no compiled output), go build skips the link.
rc=0
CGO_ENABLED=0 go build $flags -o "$dir/bin" "$pkg" >"$dir/log" 2>&1 || rc=$?
wait "$list_pid" || true
cat "$dir/log" >&2

mv -f "$dir/started.new" "$dir/started"
printf '%s\n' "$key" >"$dir/key"
if [ -s "$dir/inputs.new" ] && [ -n "$precise" ]; then
  mv -f "$dir/inputs.new" "$dir/inputs"
else
  rm -f "$dir/inputs.new" "$dir/inputs" # nothing recorded, or mtimes too coarse: never reuse
fi

if [ "$rc" -ne 0 ]; then
  { echo "fail $(date +%s)"; cat "$dir/log"; } >"$dir/state.new"
  mv -f "$dir/state.new" "$dir/state"
  exit "$rc"
fi
echo ok >"$dir/state.new"
mv -f "$dir/state.new" "$dir/state"
deliver
