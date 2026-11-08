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
# in forge's workload model). Every save used to make each watcher run an
# identical `go build` at the same moment: N concurrent compiles and N links of
# one binary. Go's build cache does not dedupe work that is in flight in
# another process, so the N builds raced each other for the CPU instead.
#
# WHAT THIS DOES
# --------------
# Under an exclusive lock, the first caller builds into a shared path
# (tmp/air-build/<package>/bin). Every later caller takes the lock and reuses
# that build when its recorded inputs still hash to its recorded digest and the
# toolchain key matches; otherwise it builds.
#
#   inputs  — what the build read, as `go list -deps` reports it: each
#             non-standard package's directory (its files and entry names; its
#             subdirectories are other packages, listed on their own when
#             imported), each //go:embed pattern's directory WHOLE (any depth,
#             since a directory pattern embeds its whole tree), and the
#             go.mod/go.sum/go.work of every module outside the read-only module
#             cache. So a go.mod `replace`, a go.work member and an embed are all
#             covered, and no hand-kept exclusion list can drift.
#   digest  — a content hash of those inputs: entry names, and every file's bytes
#   key     — the toolchain and flags (go version, GOOS/GOARCH, GOFLAGS, …)
#   state   — ok, or fail plus the build's output
#
# Reuse is decided by bytes, not mtimes, so a save that changed nothing (an
# unchanged file re-saved, a branch switched away and back) reuses the build
# without running go at all, and changed bytes under an old mtime (`cp -p`,
# `touch -r`, an archive restore) still rebuild. And a save whose bytes changed
# but whose compiled output did not (a comment appended) costs no link: go build
# finds the published binary already up to date and leaves it be.
#
# A BUILD IS RECORDED REUSABLE ONLY WHEN ITS INPUT GRAPH HELD STILL
# -----------------------------------------------------------------
# go list and go build each read the tree at their own moments, so an edit
# landing mid-build could leave an inventory that does not describe the graph
# the build compiled (an import added after go list read a file and before go
# build did), and a later caller would trust it. So the inventory and its digest are taken BEFORE go build
# starts, complete; AFTER it ends, the digest is taken again over the same
# inventory and go list runs again. The build is recorded reusable only when
# the list was complete, both digests are equal, the second list's inventory
# equals the first (which catches a graph input no listed path holds, such as a
# go.mod created in an unlisted directory, moving a module boundary), and no
# input was written after the build began (which also catches an edit reverted
# within the build). Every byte the build could have read was then the same
# from its start to its end, and the binary is the build of exactly those
# bytes. Otherwise the binary still goes to this caller, but nothing is
# recorded, and the next caller builds again.
#
# A failed build never replaces a published binary: go build replaces its
# target only on success. A failure recorded as above (`state=fail`) makes a
# caller arriving for the same source print the same error and exit non-zero,
# rather than take the last good binary as if it were fresh (Air then keeps the
# old binary, exactly as before). A failure is reused only for
# AIR_BUILD_FAIL_TTL seconds, so a transient one (a killed build, a full disk)
# is retried by the next caller rather than remembered. A failure whose go list
# reported package errors (a syntax error, a missing import) is never
# recorded: that list can omit inputs, so every caller builds until it is fixed.
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
# One go invocation for the key and the two paths the inventory skips (each go
# run costs a reuse ~0.1s).
env=$(go env GOVERSION GOOS GOARCH GOFLAGS GOWORK GOEXPERIMENT GOARM64 GOAMD64 GOMODCACHE GOROOT)
key=$(printf '%s\n' "air-build v2" "$pkg" "CGO_ENABLED=0" "$flags" "$env")
gomodcache=$(printf '%s\n' "$env" | tail -n 2 | head -n 1)
goroot=$(printf '%s\n' "$env" | tail -n 1)

# deliver hard-links (or, across filesystems, copies) the published binary to
# the caller's output path through a rename, so a running process is never
# handed a half-written file.
deliver() {
  # Already this build (a hard link): GNU mv refuses to move a file onto
  # another link of itself. -ef is outside POSIX but every sh this runs under
  # (dash, bash, busybox ash, macOS sh) implements it.
  # shellcheck disable=SC3013
  [ "$dir/bin" -ef "$out" ] && return 0
  mkdir -p "$(dirname "$out")"
  rm -f "$out.tmp"
  ln "$dir/bin" "$out.tmp" 2>/dev/null || cp "$dir/bin" "$out.tmp"
  mv -f "$out.tmp" "$out"
}

if command -v sha256sum >/dev/null 2>&1; then
  hash=sha256sum
elif command -v shasum >/dev/null 2>&1; then
  hash="shasum -a 256"
else
  hash="cksum"
fi

# An inventory holds one input per line: "1 <path>" is read to depth 1 (a
# package directory's files and entry names, or a single file), "r <path>" at
# any depth (an embed pattern's directory). split_inputs <inventory> writes
# the two path lists, NUL-separated, to $dir/in.1 and $dir/in.r.
split_inputs() {
  sed -n 's/^1 //p' "$1" | tr '\n' '\0' >"$dir/in.1"
  sed -n 's/^r //p' "$1" | tr '\n' '\0' >"$dir/in.r"
}

# digest prints one content hash of the inputs in inventory $1: every entry
# name and the bytes of every file. Prints nothing, and fails, when a path is
# gone or a hash cannot be taken.
digest() {
  split_inputs "$1"
  : >"$dir/digest.list"
  if [ -s "$dir/in.1" ]; then
    xargs -0 sh -c 'h=$1; shift
      find "$@" -maxdepth 1 -print && find "$@" -maxdepth 1 ! -type d -exec $h {} +' \
      sh "$hash" <"$dir/in.1" >>"$dir/digest.list" || return 1
  fi
  if [ -s "$dir/in.r" ]; then
    xargs -0 sh -c 'h=$1; shift
      find "$@" -print && find "$@" ! -type d -exec $h {} +' \
      sh "$hash" <"$dir/in.r" >>"$dir/digest.list" || return 1
  fi
  d=$(LC_ALL=C sort "$dir/digest.list" | $hash) || return 1
  d=${d%% *}
  [ -n "$d" ] || return 1
  printf '%s\n' "$d"
}

# written_since prints every input of inventory $1 written after file $2 was
# (a directory counts when entries were added, removed or renamed in it).
# Fails when a path is gone.
written_since() {
  split_inputs "$1"
  if [ -s "$dir/in.1" ]; then
    xargs -0 sh -c 's=$1; shift
      find "$@" -maxdepth 0 -newer "$s" -print && find "$@" -maxdepth 1 ! -type d -newer "$s" -print' \
      sh "$2" <"$dir/in.1" || return 1
  fi
  if [ -s "$dir/in.r" ]; then
    xargs -0 sh -c 's=$1; shift; find "$@" -newer "$s" -print' sh "$2" <"$dir/in.r" || return 1
  fi
}

# inventory <file> writes what building $pkg reads, as `go list -deps` reports
# it now, sorted. Exits 0 for a complete list, 3 for a list that parsed but
# reported package errors (never recorded: it can omit what the failing
# package reads), anything else for no usable list (go list's stderr is left
# in $dir/list.err).
inventory() {
  # One line per fact: "D <package dir>", "P <package dir>|<embed pattern>",
  # "M <go.mod>", "X <import path>" for a package with errors. awk turns an
  # embed pattern into the directory before its first wildcard (the package
  # directory itself for a pattern like *.yaml), read at any depth, and a
  # go.mod into it and its go.sum and go.work siblings. Paths in the read-only
  # module cache and GOROOT never change. A line in no known form, or no
  # package at all, fails the list.
  # shellcheck disable=SC2016 # a go template, not a shell expansion
  CGO_ENABLED=0 go list -e -deps -f '{{if .Incomplete}}X {{.ImportPath}}
{{end}}{{if not .Standard}}D {{.Dir}}
{{range .EmbedPatterns}}P {{$.Dir}}|{{.}}
{{end}}{{with .Module}}{{with .GoMod}}M {{.}}
{{end}}{{end}}{{end}}' "$pkg" >"$dir/list.out" 2>"$dir/list.err" || return 1
  lrc=0
  awk -v cache="$gomodcache/" -v root="$goroot/" '
    NF == 0 { next }
    !/^[DPMX] ./ { bad = 1; next }
    { kind = substr($0, 1, 1); p = substr($0, 3) }
    kind == "X" { incomplete = 1; next }
    kind == "D" { pkgs++; out("1", p) }
    kind == "P" {
      bar = index(p, "|")
      if (bar < 2) { bad = 1; next }
      d = substr(p, 1, bar - 1); pat = substr(p, bar + 1)
      sub(/^all:/, "", pat)
      n = split(pat, parts, "/")
      for (i = 1; i <= n && parts[i] !~ /[*?[\\]/; i++) d = d "/" parts[i]
      out("r", d)
    }
    kind == "M" {
      out("1", p); sub(/\.mod$/, "", p)
      out("1", p ".sum"); out("1", p ".work"); out("1", p ".work.sum")
    }
    function out(tag, path) {
      if (path == "" || index(path, cache) == 1 || index(path, root) == 1) return
      print tag " " path
    }
    END { if (bad || pkgs == 0) exit 1; if (incomplete) exit 3 }' "$dir/list.out" >"$dir/list.paths" || lrc=$?
  [ "$lrc" -eq 0 ] || [ "$lrc" -eq 3 ] || return 1
  LC_ALL=C sort -u "$dir/list.paths" >"$dir/list.sorted" || return 1
  while IFS= read -r line; do
    [ -e "${line#? }" ] && printf '%s\n' "$line" # drop a go.sum or go.work that does not exist
  done <"$dir/list.sorted" >"$1"
  return "$lrc"
}

# unchanged succeeds when the recorded build read exactly the bytes there are
# now, with the same toolchain and flags.
unchanged() {
  [ -s "$dir/inputs" ] && [ -s "$dir/digest" ] && [ -f "$dir/state" ] && [ -f "$dir/key" ] || return 1
  [ "$(cat "$dir/key")" = "$key" ] || return 1
  now=$(digest "$dir/inputs") || return 1
  [ "$now" = "$(cat "$dir/digest")" ]
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
# Nothing is reusable until this build proves itself, so a crash from here on
# leaves the next caller building.
rm -f "$dir/state" "$dir/inputs" "$dir/digest" "$dir/started"
: >"$dir/started"
# Linux stamps files from a coarse clock (one tick, up to 10ms), so a write in
# the same tick as this stamp has an equal mtime and does not count as newer.
# Waiting out a tick before anything is read puts such a write before the
# build, where it belongs.
sleep 0.02
# The written-during-the-build check rests on find telling apart two mtimes
# 20ms apart. BusyBox find compares whole seconds and some filesystems store
# no more: there nothing is recorded, and every caller builds.
: >"$dir/probe"
precise=$(find "$dir/probe" -newer "$dir/started" -print)

# Before: what the build is about to read, and its bytes.
pre_rc=0
inventory "$dir/inputs.pre" || pre_rc=$?
case $pre_rc in 0 | 3) digest "$dir/inputs.pre" >"$dir/digest.pre" || pre_rc=1 ;; esac

# Straight onto the published path: go build links in its own work directory
# and replaces the target only on success, with a new file (remove, then
# rename), so a failed build leaves it alone and a running process, a hard link
# to the old file, is never touched. And when the target already IS this
# build (a save that changed no compiled output), go build skips the link.
rc=0
CGO_ENABLED=0 go build $flags -o "$dir/bin" "$pkg" >"$dir/log" 2>&1 || rc=$?
cat "$dir/log" >&2

# After: the graph listed again, and the same bytes again over the pre-build
# inventory. Bytes that moved under the build (an import added, a file saved)
# change the digest. A graph input outside that inventory (a go.mod created in
# a directory no listed package owns, which moves a module boundary) changes
# no listed byte, so go list runs again and its inventory must equal the first.
post_digest=
post_rc=0
case $pre_rc in
  0 | 3)
    post_digest=$(digest "$dir/inputs.pre") || post_digest=
    inventory "$dir/inputs.post" || post_rc=$?
    ;;
esac

printf '%s\n' "$key" >"$dir/key"
why=
if [ "$pre_rc" -ne 0 ] && [ "$pre_rc" -ne 3 ]; then
  why="go list failed for $pkg"
  cat "$dir/list.err" >&2 2>/dev/null || true
elif [ "$pre_rc" -eq 3 ]; then
  # An incomplete list can omit what a package with errors reads (a package
  # go list could not locate has no directory to list), so a later edit there
  # would not change the digest: neither a success nor a failure is pinned to it.
  why="go list reported package errors"
elif [ -z "$post_digest" ] || [ "$post_digest" != "$(cat "$dir/digest.pre")" ]; then
  why="its inputs changed while it ran"
elif [ "$post_rc" -ne "$pre_rc" ] || ! cmp -s "$dir/inputs.pre" "$dir/inputs.post"; then
  why="its package graph changed while it ran"
elif [ -z "$precise" ]; then
  why="this filesystem's mtimes are too coarse to see writes during it"
elif ! written=$(written_since "$dir/inputs.pre" "$dir/started") || [ -n "$written" ]; then
  why="its inputs were written while it ran"
fi
if [ -z "$why" ]; then
  mv -f "$dir/inputs.pre" "$dir/inputs"
  mv -f "$dir/digest.pre" "$dir/digest"
else
  echo "air-build: not recording this build of $pkg ($why); the next caller builds again" >&2
fi
rm -f "$dir/inputs.pre" "$dir/inputs.post" "$dir/digest.pre" \
  "$dir/list.out" "$dir/list.err" "$dir/list.paths" "$dir/list.sorted" "$dir/digest.list" "$dir/in.1" "$dir/in.r"

if [ "$rc" -ne 0 ]; then
  { echo "fail $(date +%s)"; cat "$dir/log"; } >"$dir/state.new"
  mv -f "$dir/state.new" "$dir/state"
  exit "$rc"
fi
echo ok >"$dir/state.new"
mv -f "$dir/state.new" "$dir/state"
deliver
