#!/usr/bin/env bash
# test-release-lock.sh — hermetic test of scripts/release-lock.sh against a bare git
# remote: one holder at a time, a refused second holder leaves the first's lock alone,
# release drops only our own lock, a stale lock is replaced. No network.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
fails=0
check() { if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want '$2', got '$3'"; fails=$((fails + 1)); fi; }
git init -q --bare "$T/origin.git"
for c in a b; do git init -q "$T/$c" && git -C "$T/$c" remote add origin "$T/origin.git"; done
lock() { (cd "$T/$1" && . "$HERE/release-lock.sh" && shift && eval "$*"); }
held() { git -C "$T/a" ls-remote origin refs/release-lock/v9.9.9 | cut -f1; }

check a-takes-it 0 "$(lock a 'release_lock_acquire v9.9.9 2>/dev/null; echo $?')"
first="$(held)"
check b-refused 1 "$(lock b 'release_lock_acquire v9.9.9 2>/dev/null; echo $?')"
check b-left-a-alone "$first" "$(held)"
check b-says-why 1 "$(lock b 'release_lock_acquire v9.9.9 2>&1 | grep -c "already in progress"')"
check held-env-is-a-noop 0 "$(RELEASE_LOCK_HELD=v9.9.9 lock b 'release_lock_acquire v9.9.9; echo $?')"
check a-take-and-release "" "$(lock a 'release_lock_acquire v9.9.8 && release_lock_release'; git -C "$T/a" ls-remote origin refs/release-lock/v9.9.8)"
check stale-replaced 0 "$(RELEASE_LOCK_STALE_HOURS=0 lock b 'release_lock_acquire v9.9.9 2>/dev/null; echo $?')"
[ "$(held)" != "$first" ] && check stale-new-holder yes yes || check stale-new-holder yes no
check no-remote-refuses 1 "$(git init -q "$T/c" && lock c 'release_lock_acquire v1.0.0 2>/dev/null; echo $?')"
# Regression: same holder text, frozen commit dates (identical but for the nonce). The
# second acquisition must still be refused, and a stale replace must get a new sha.
export GIT_AUTHOR_DATE=2001-01-01T00:00:00Z GIT_COMMITTER_DATE=2001-01-01T00:00:00Z
check frozen-a-takes 0 "$(lock a 'release_lock_acquire v7.7.7 2>/dev/null; echo $?')"
frozen="$(git -C "$T/a" ls-remote origin refs/release-lock/v7.7.7 | cut -f1)"
check frozen-b-refused 1 "$(lock b 'release_lock_acquire v7.7.7 2>/dev/null; echo $?')"
check frozen-stale-replaces 0 "$(RELEASE_LOCK_STALE_HOURS=0 lock b 'release_lock_acquire v7.7.7 2>/dev/null; echo $?')"
[ "$(git -C "$T/a" ls-remote origin refs/release-lock/v7.7.7 | cut -f1)" != "$frozen" ] && check frozen-stale-distinct-sha yes yes || check frozen-stale-distinct-sha yes no
unset GIT_AUTHOR_DATE GIT_COMMITTER_DATE
[ "$fails" = 0 ] && echo PASS || { echo "$fails failure(s)"; exit 1; }
