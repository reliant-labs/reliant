#!/usr/bin/env bash
# release-lock.sh — one release cutter per repository per machine. Sourced, never run.
#
# Every script that cuts, tags or publishes a release takes its repository's lock
# first, so a second cutter fails fast and is told who holds it instead of racing
# the first (two chats deployed prod at once on 2026-10-24; web-runtime was
# released five times in 14 hours by different chats). The same file is carried
# by control-plane (scripts/lib/), reliant (scripts/lib/) and forge (scripts/lib/):
# keep the copies identical.
#
#   release_lock <name> [<what>]   take <dir>/<name>.lock for the rest of the calling
#                                  script; on contention print the holder and return 75
#   release_lock_status <name>     return 0 when free; print the holder and return 75
#                                  when held (a dry run reports, it does not lock)
#
# The lock is flock(2) on <dir>/<name>.lock, <dir> being RELEASE_LOCK_DIR (default
# $HOME/fm-logs/release-locks): one directory shared by every checkout, worktree and
# chat on the machine. The file also holds the holder's record (pid, owner, host,
# start time, what it is doing), rewritten by each new holder.
#
# The lock is held by a small holder process that lives exactly as long as the
# calling script ($$), never by a descriptor of the script itself: a descriptor is
# inherited by every child (docker, forge, gh, a port-forward), and one child that
# outlived the script would hold the lock forever. The holder notices its script is
# gone within a second and exits, which releases the lock; a crash releases it too.
#
# Re-entrant: the holder's token is exported as RELEASE_LOCK_TOKEN_<NAME>, so a child
# of the holding script (cut-release.sh re-running itself in a private checkout, the
# release train running a cut) proceeds instead of refusing its own parent.
#
# RELEASE_LOCK_OWNER names the caller in the record (the release train sets it).

_rl_dir() { printf '%s' "${RELEASE_LOCK_DIR:-${HOME}/fm-logs/release-locks}"; }
_rl_var() { printf 'RELEASE_LOCK_TOKEN_%s' "$(printf '%s' "$1" | tr 'abcdefghijklmnopqrstuvwxyz-' 'ABCDEFGHIJKLMNOPQRSTUVWXYZ_')"; }

# _rl_try <fd>: a non-blocking exclusive flock on an open descriptor. flock(1) where it
# exists (Linux); perl's flock(2) elsewhere (macOS ships no flock(1)). A lock taken by
# a child on an inherited descriptor belongs to the shared open file, so it outlives
# the child.
_rl_try() {
  if command -v flock > /dev/null 2>&1; then
    flock -n "$1"
    return
  fi
  perl -MFcntl=:flock -e 'open(my $f, ">&=", $ARGV[0]) or exit 2; flock($f, LOCK_EX | LOCK_NB) or exit 1' "$1"
}

# _rl_free <file>: is the lock free right now? Probed in a subshell, whose descriptor
# (and so the probe's lock) is gone when it exits.
_rl_free() { [ ! -e "$1" ] || (exec 9>> "$1" && _rl_try 9) 2> /dev/null; }

_rl_field() { sed -n "s/^$2=//p" "$1" 2> /dev/null | head -n 1; }

_rl_report() {
  local name="$1" file="$2" pid
  pid="$(_rl_field "$file" pid)"
  printf 'release-lock: another release of %s is in progress; %s is held by:\n' "$name" "$file" >&2
  if [ -n "$pid" ]; then
    sed 's/^/  /' "$file" >&2
    if ! kill -0 "$pid" 2> /dev/null; then
      printf '  (pid %s has exited, but a process holding the lock is still alive: fuser -v %s)\n' "$pid" "$file" >&2
    fi
  else
    printf '  (the holder has not written its record yet)\n' >&2
  fi
  printf 'release-lock: wait for it to finish; never delete the lock file (flock releases it when its holder exits).\n' >&2
}

release_lock_status() {
  local name="$1" file
  file="$(_rl_dir)/${name}.lock"
  if _rl_free "$file"; then return 0; fi
  _rl_report "$name" "$file"
  return 75
}

release_lock() {
  local name="$1" what="${2:-}" dir file var tok fifo reply parent=$$
  case "$name" in '' | *[!a-z0-9-]*)
    printf 'release-lock: bad lock name %s\n' "$name" >&2
    return 2
    ;;
  esac
  dir="$(_rl_dir)"
  mkdir -p "$dir" || {
    printf 'release-lock: cannot create %s\n' "$dir" >&2
    return 2
  }
  file="$dir/${name}.lock"
  var="$(_rl_var "$name")"
  tok="${!var:-}"
  if [ -n "$tok" ] && ! _rl_free "$file" && [ "$(_rl_field "$file" token)" = "$tok" ]; then
    printf 'release-lock: %s is held by the release that started this one (pid %s); continuing\n' "$name" "$(_rl_field "$file" pid)" >&2
    return 0
  fi
  tok="${parent}.$(date +%s).${RANDOM}"
  fifo="$(mktemp -u "${TMPDIR:-/tmp}/release-lock.XXXXXXXX")"
  mkfifo "$fifo" || {
    printf 'release-lock: cannot create %s\n' "$fifo" >&2
    return 2
  }
  (
    trap - EXIT INT TERM
    exec 7>> "$file" || {
      echo error > "$fifo"
      exit 2
    }
    if ! _rl_try 7 2> /dev/null; then
      echo busy > "$fifo"
      exit 75
    fi
    {
      printf 'token=%s\n' "$tok"
      printf 'pid=%s\n' "$parent"
      printf 'owner=%s\n' "${RELEASE_LOCK_OWNER:-$(id -un 2> /dev/null || echo unknown)}"
      printf 'host=%s\n' "$(hostname 2> /dev/null || echo unknown)"
      printf 'started=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
      printf 'cwd=%s\n' "$PWD"
      printf 'what=%s\n' "${what:-$0}"
    } > "$file"
    echo ok > "$fifo"
    exec < /dev/null > /dev/null 2>&1
    while kill -0 "$parent" 2> /dev/null; do sleep 1; done
  ) < /dev/null > /dev/null 2>&1 &
  disown $! 2> /dev/null || true
  read -r reply < "$fifo"
  rm -f "$fifo"
  case "$reply" in
    ok)
      export "$var=$tok"
      return 0
      ;;
    busy)
      _rl_report "$name" "$file"
      return 75
      ;;
    *)
      printf 'release-lock: cannot open %s\n' "$file" >&2
      return 2
      ;;
  esac
}
