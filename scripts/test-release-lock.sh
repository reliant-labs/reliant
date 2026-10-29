#!/usr/bin/env bash
# Hermetic self-test of scripts/release-lock.sh: one release per repository at a time,
# a heartbeat that keeps a held lock live, a dead holder's stale lock replaced, and the
# holder's re-check of main on release. Throwaway git repos and a fake gh; no network.
set -euo pipefail
src="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/release-lock.sh"
fx="$(mktemp -d)"
trap 'rm -rf "$fx"' EXIT
export GIT_AUTHOR_NAME=F GIT_AUTHOR_EMAIL=f@x.invalid GIT_COMMITTER_NAME=F GIT_COMMITTER_EMAIL=f@x.invalid
git init -q --bare "$fx/origin.git"
git -C "$fx/origin.git" symbolic-ref HEAD refs/heads/main
git init -q -b main "$fx/a"
git -C "$fx/a" commit -q --allow-empty -m base
git -C "$fx/a" tag -a v1.0.0 -m v1.0.0
git -C "$fx/a" remote add origin "$fx/origin.git"
git -C "$fx/a" push -q origin main refs/tags/v1.0.0
git clone -q "$fx/origin.git" "$fx/b"
mkdir -p "$fx/bin"
cat > "$fx/bin/gh" << 'GH'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$GH_CALLS"
case "$*" in
  'pr list '*'--label release:asap'*) ;;
  'pr list '*'--limit 1 '*) echo 7 ;;
  'pr view '*) echo false ;;
  'pr edit '*) ;;
  *) echo "unexpected gh: $*" >&2; exit 90 ;;
esac
GH
chmod +x "$fx/bin/gh"
export PATH="$fx/bin:$PATH" GH_CALLS="$fx/gh-calls" RELEASE_LOCK_SLUG=acme/repo
: > "$GH_CALLS"
fail() { echo "FAIL: $*" >&2; exit 1; }
lock_sha() { git -C "$fx/a" ls-remote origin refs/release-lock/release | cut -f1; }
# in <clone> <script>: run a script that sourced the lock in its own shell, like a cutter.
# shellcheck source=release-lock.sh
holder() { (cd "$fx/$1" && unset RELEASE_LOCK_HELD && source "$src" && eval "$2"); }

# 1. A free lock is taken; a second cutter is refused and told who holds it; the
#    holder's release drops it.
[ "$(holder a release_lock_status)" = free ] || fail "status of a missing lock is not free"
holder a 'RELEASE_LOCK_RECHECK=off RELEASE_LOCK_HEARTBEAT_SECS=1; release_lock_acquire v1.0.1
  [ -n "$(git ls-remote origin refs/release-lock/release)" ] || exit 11
  case "$(release_lock_status)" in "live "*"release lock: v1.0.1 by "*) ;; *) exit 12 ;; esac
  first="$(git ls-remote origin refs/release-lock/release | cut -f1)"
  (cd '"$fx"'/b && unset RELEASE_LOCK_HELD && source '"$src"' && ! release_lock_acquire v1.0.2 2> '"$fx"'/refused) || exit 13
  # The heartbeat replaces the lock commit while it is held.
  for _ in $(seq 1 30); do [ "$(git ls-remote origin refs/release-lock/release | cut -f1)" != "$first" ] && break; sleep 0.5; done
  [ "$(git ls-remote origin refs/release-lock/release | cut -f1)" != "$first" ] || exit 14
  release_lock_release
  [ -z "$(git ls-remote origin refs/release-lock/release)" ] || exit 15' || fail "acquire/refuse/heartbeat/release (exit $?)"
grep -qF 'already in progress: release lock: v1.0.1 by ' "$fx/refused" || { cat "$fx/refused" >&2; fail "the refusal does not name the holder"; }

# 2. A nested run (cut-release.sh --cut-recorded's private checkout) inherits a held
#    lock: it neither takes nor drops it.
holder a 'RELEASE_LOCK_RECHECK=off; release_lock_acquire v1.0.1
  bash -c "source '"$src"' && release_lock_acquire v1.0.1 && release_lock_release" || exit 21
  [ -n "$(git ls-remote origin refs/release-lock/release)" ] || exit 22
  release_lock_release' || fail "a nested run with RELEASE_LOCK_HELD touched the lock (exit $?)"

# 3. A lock with no heartbeat for RELEASE_LOCK_STALE_MINUTES is a dead holder's: status
#    says stale and the next cutter replaces it.
tree="$(git -C "$fx/a" mktree < /dev/null)"
old="$(echo 'release lock: v0.9.9 by dead@box pid 1 cut-release.sh' | GIT_COMMITTER_DATE="$(($(date +%s) - 1200)) +0000" git -C "$fx/a" commit-tree "$tree")"
git -C "$fx/a" push -q origin "$old:refs/release-lock/release"
case "$(holder a release_lock_status)" in "stale "*"dead@box"*) ;; *) fail "a 20-minute-old lock is not stale" ;; esac
holder b 'RELEASE_LOCK_RECHECK=off; release_lock_acquire v1.0.3 2> '"$fx"'/replaced
  case "$(release_lock_status)" in "live "*"v1.0.3"*) ;; *) exit 31 ;; esac
  release_lock_release' || fail "a stale lock was not replaced (exit $?)"
grep -qF 'replaced a stale lock' "$fx/replaced" || fail "the replacement was silent"
[ -z "$(lock_sha)" ] || fail "the replacing holder did not drop its lock"

# 3b. The stale verdict and the lease are one commit. Another process sharing this
#     checkout replaces the stale lock with a live one (and moves every shared scratch
#     ref to it) between this cutter's read and its push: the push must be refused and
#     the live holder's lock left alone.
old="$(echo 'release lock: v0.9.9 by dead@box pid 1 cut-release.sh' | GIT_COMMITTER_DATE="$(($(date +%s) - 1200)) +0000" git -C "$fx/a" commit-tree "$tree")"
git -C "$fx/a" push -q origin "$old:refs/release-lock/release"
# The racer shares b's object store (it is "another process in this checkout").
live="$(echo 'release lock: v1.0.5 by racer@box pid 2 cut-release.sh' | git -C "$fx/b" commit-tree "$tree")"
holder b 'eval "$(declare -f _release_lock_read | sed "1s/_release_lock_read/_real_read/")"
  _release_lock_read() { local r; r="$(_real_read)" || return 1
    git push -q origin --force-with-lease=refs/release-lock/release:'"$old"' '"$live"':refs/release-lock/release
    git update-ref refs/release-chain/lock '"$live"'
    printf "%s\n" "$r"; }
  RELEASE_LOCK_RECHECK=off; ! release_lock_acquire v1.0.6 2> /dev/null' || fail "a cutter replaced a lock someone took after it judged the old one stale"
[ "$(lock_sha)" = "$live" ] || fail "the racing holder's live lock was overwritten"
git -C "$fx/a" push -q origin :refs/release-lock/release

# 4. Release re-checks main: nothing merged while held -> no label; a merge landed
#    while held and main is due -> main's newest merged PR is labelled release:asap.
: > "$GH_CALLS"
holder a 'RELEASE_EVERY=1; release_lock_acquire v1.0.4; release_lock_release'
grep -q 'pr edit' "$GH_CALLS" && fail "a release with nothing merged behind it re-started itself"
: > "$GH_CALLS"
holder a 'RELEASE_EVERY=1; release_lock_acquire v1.0.4
  git commit -q --allow-empty -m "merged while held" && git push -q origin HEAD:main
  release_lock_release 2> '"$fx"'/recheck'
grep -qx 'pr edit 7 -R acme/repo --add-label release:asap' "$GH_CALLS" || { cat "$GH_CALLS" >&2; fail "a coalesced merge was not re-started"; }
grep -qF 'labelled #7 release:asap' "$fx/recheck" || fail "the re-check was silent"
# ...and not when main moved but is not due (below RELEASE_EVERY, no asap PR).
: > "$GH_CALLS"
holder a 'RELEASE_EVERY=10; release_lock_acquire v1.0.4
  git commit -q --allow-empty -m "another" && git push -q origin HEAD:main
  release_lock_release'
grep -q 'pr edit' "$GH_CALLS" && fail "a not-due main was labelled"
echo "release-lock self-test: PASS"
