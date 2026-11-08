#!/bin/sh
# air-build.sh — build one Go main package ONCE per save, however many Air
# watchers ask for it, and hand each watcher its own copy of the binary.
#
#   usage: scripts/air-build.sh <main package> <output path>
#   e.g.   scripts/air-build.sh ./cmd/control-plane ./tmp/admin-server/control-plane
#
# The same file lives in control-plane and reliant; change both together.
#
# WHY THIS EXISTS
# ---------------
# Several dev processes are the SAME binary run under different subcommands,
# each with its own Air watcher (separate processes, ports, logs and readiness
# in forge's workload model). Every save made each watcher run an identical
# `go build` at the same moment: N concurrent compiles and N links of one
# binary. Go's build cache does not dedupe work that is in flight in another
# process, so the N builds raced each other for the CPU and memory instead.
#
# WHAT THIS DOES
# --------------
# Every caller takes one exclusive lock, runs
#   CGO_ENABLED=0 go build -trimpath -buildvcs=false -o tmp/air-build/<pkg>/bin <pkg>
# and, when it succeeds, links that shared binary to its own output path. The
# first caller after a save does the real compile and link. The callers queued
# behind it then find every package in Go's cache and the shared binary
# already up to date, so their go build neither relinks nor rewrites it (it
# only refreshes the mtime) and takes about a second. So one compile and one
# link per save.
#
# WHY GO DECIDES WHAT IS STALE
# ----------------------------
# Whether a binary is current is exactly what go build's action IDs answer:
# every source file, embedded file, go.mod, build constraint, toolchain and
# flag that went into it. An earlier version of this script kept its own
# record of the build's inputs (go list, mtimes, digests) so callers could
# skip go build. Each review found another input it missed (an import added
# while the list ran, nested embed directories, a new module boundary),
# because it was re-implementing the cache Go already has. Asking go costs
# about a second per extra caller and is correct by construction.
#
# go build writes its target only on success, and then as a new file (remove,
# then rename). So a failed build leaves the shared binary, and every running
# process hard-linked to it, untouched. That caller exits with go's status,
# and the next caller simply tries again.
#
# Builds use -trimpath: compiled packages are then keyed by module path, not by
# this checkout's absolute path, so worktrees at the same commit share them in
# the one GOCACHE. (Stack traces name files by module path, too.)
#
# THE LOCK
# --------
# A kernel lock that is released when its holder dies, so a killed build
# never wedges the next save: flock(1) (util-linux, and BusyBox in the alpine
# dev image), else /usr/bin/lockf (every macOS; Homebrew's flock is not
# guaranteed there), else perl's flock. A mkdir lock would need stale-pid
# recovery, which cannot be made race-free. POSIX sh, so it also runs in the
# alpine dev image, which has no bash.
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

CGO_ENABLED=0 go build -trimpath -buildvcs=false -o "$dir/bin" "$pkg"

# Link the shared binary to the caller's output through a rename, so a running
# process is never handed a half-written file. Already that file (a hard link
# from an earlier save whose binary did not change): nothing to do, and GNU mv
# refuses to move a file onto another link of itself. -ef is outside POSIX but
# every sh this runs under (dash, bash, busybox ash, macOS sh) implements it.
# shellcheck disable=SC3013
[ "$dir/bin" -ef "$out" ] && exit 0
mkdir -p "$(dirname "$out")"
rm -f "$out.tmp"
ln "$dir/bin" "$out.tmp" 2>/dev/null || cp "$dir/bin" "$out.tmp"
mv -f "$out.tmp" "$out"
