#!/usr/bin/env bash
# Sourced, not run; the shebang names its dialect for shellcheck.
# release-lock.sh — ONE release of a repository at a time, across machines and across
# people and pipelines. Sourced by the release scripts (forge scripts/release-forge.sh,
# reliant scripts/release-tag.sh, control-plane scripts/cut-release.sh and
# release-local.sh promote; the same file in each repo).
#
# The lock is one ref on origin, refs/release-lock/release, pushed with
# --force-with-lease=<ref>: ("must not exist"), which GitHub applies atomically: of two
# cutters exactly one creates it and the other is refused, told who holds it. It points
# at an empty-tree commit whose message names the owner and version, and whose committer
# date is the last HEARTBEAT: while held, a background loop replaces it (compare-and-swap
# on its own previous sha) every RELEASE_LOCK_HEARTBEAT_SECS (60). A lock whose heartbeat
# is older than RELEASE_LOCK_STALE_MINUTES (15) is a dead holder and may be replaced.
# The release workflow reads the same ref before it starts a captain: a live lock makes
# the run exit at once as "release in progress by <owner>; coalesced".
#
# When the holder releases it, it RE-CHECKS main (release_lock_recheck): if main moved
# while it held the lock (the merges whose runs were coalesced) and main is now due (a
# release:asap PR, or RELEASE_EVERY merges since the newest tag), it re-applies
# release:asap to main's newest merged PR, which starts the release workflow again,
# judged at main's tip. So a coalesced run is never lost, and a failed cut with nothing
# merged behind it never re-starts itself.
#
#   release_lock_acquire <version>   take it, or print who holds it and return 1
#   release_lock_release             drop it if still ours, stop the heartbeat, re-check main
#
# A script that re-runs itself (cut-release.sh --cut-recorded) exports
# RELEASE_LOCK_HELD=1 to its child, which then neither takes nor drops it.
# RELEASE_LOCK=off skips it (fixtures with no remote only); RELEASE_LOCK_RECHECK=off skips
# the re-check (tests).
RELEASE_LOCK_REF="refs/release-lock/release" RELEASE_LOCK_STATE="" RELEASE_LOCK_HB_PID=""
RELEASE_LOCK_MSG="" RELEASE_LOCK_MAIN=""

_release_lock_commit() { # <message> -> a fresh commit sha (nonce: never the same sha twice)
  local tree nonce
  tree="$(git mktree < /dev/null)" || return 1
  nonce="$(cat /proc/sys/kernel/random/uuid 2> /dev/null || od -An -N16 -tx1 /dev/urandom 2> /dev/null | tr -d ' \n')"
  [ -n "$nonce" ] || nonce="${RANDOM}${RANDOM}${RANDOM}-$(date +%s%N)"
  # The owner line alone is the subject (%s); the nonce is the body.
  printf '%s\n\nnonce %s\n' "$1" "$nonce" | GIT_AUTHOR_NAME=release-lock GIT_AUTHOR_EMAIL=release-lock@invalid \
    GIT_COMMITTER_NAME=release-lock GIT_COMMITTER_EMAIL=release-lock@invalid GIT_COMMITTER_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    git commit-tree "$tree"
}

# release_lock_status -> "free" | "live <age-seconds> <owner line>" | "stale <age> <owner>"
# | "unknown" (origin could not be asked). Read-only; the workflow uses the same rule.
release_lock_status() {
  local remote="${RELEASE_LOCK_REMOTE:-origin}" stale_m="${RELEASE_LOCK_STALE_MINUTES:-15}" sha age owner
  sha="$(timeout 30 git ls-remote "$remote" "$RELEASE_LOCK_REF" 2> /dev/null)" || { echo unknown; return 0; }
  sha="${sha%%[[:space:]]*}"
  [ -n "$sha" ] || { echo free; return 0; }
  timeout 30 git fetch -q "$remote" "+${RELEASE_LOCK_REF}:refs/release-chain/lock" 2> /dev/null || { echo unknown; return 0; }
  age=$(($(date +%s) - $(git log -1 --format=%ct refs/release-chain/lock 2> /dev/null || date +%s)))
  owner="$(git log -1 --format=%s refs/release-chain/lock 2> /dev/null)"
  if [ "$age" -ge $((stale_m * 60)) ]; then echo "stale $age $owner"; else echo "live $age $owner"; fi
}

release_lock_acquire() {
  local version="$1" remote="${RELEASE_LOCK_REMOTE:-origin}" msg sha old st
  [ "${RELEASE_LOCK:-on}" != off ] || return 0
  [ -z "${RELEASE_LOCK_HELD:-}" ] || return 0
  msg="release lock: ${version} by $(id -un 2> /dev/null || echo ?)@$(hostname 2> /dev/null || echo ?) pid $$ ${RELIANT_RUN_ID:+run ${RELIANT_RUN_ID} }$(basename "${0}")"
  sha="$(_release_lock_commit "$msg")" || return 1
  if ! git push -q "$remote" "${sha}:${RELEASE_LOCK_REF}" --force-with-lease="${RELEASE_LOCK_REF}:" > /dev/null 2>&1; then
    st="$(release_lock_status)"
    case "$st" in
      stale\ *)
        old="$(git rev-parse refs/release-chain/lock)"
        if git push -q "$remote" "${sha}:${RELEASE_LOCK_REF}" --force-with-lease="${RELEASE_LOCK_REF}:${old}" > /dev/null 2>&1; then
          echo "release-lock: replaced a stale lock (no heartbeat for $(($(echo "$st" | cut -d' ' -f2) / 60)) min: $(echo "$st" | cut -d' ' -f3-))" >&2
        else
          echo "release-lock: another cutter took the stale lock first; refusing" >&2; return 1
        fi ;;
      live\ *)
        echo "release-lock: a release of this repository is already in progress: $(echo "$st" | cut -d' ' -f3-) (heartbeat $(echo "$st" | cut -d' ' -f2)s ago)." >&2
        echo "  Wait for it; it re-checks main when it finishes. If its holder is dead, the lock expires ${RELEASE_LOCK_STALE_MINUTES:-15} min after its last heartbeat." >&2
        return 1 ;;
      *)
        echo "release-lock: cannot take ${RELEASE_LOCK_REF} on ${remote} (no push access, or the network); refusing to release unlocked" >&2
        return 1 ;;
    esac
  fi
  RELEASE_LOCK_STATE="$(mktemp)" && printf '%s\n' "$sha" > "$RELEASE_LOCK_STATE"
  RELEASE_LOCK_MSG="$msg"
  RELEASE_LOCK_MAIN="$(timeout 30 git ls-remote "$remote" refs/heads/main 2> /dev/null | cut -f1 || true)"
  export RELEASE_LOCK_HELD=1
  # The heartbeat: replace our own lock commit (CAS on the previous one) until released,
  # or until this shell is gone. Detached from every output, so it never holds a pipe.
  # TERM ends it between commands, never inside a push (bash runs a trap only after the
  # foreground child), and at once while it sleeps (wait is interruptible).
  (
    trap 'exit 0' TERM
    parent=$$ hb="${RELEASE_LOCK_HEARTBEAT_SECS:-60}"
    while sleep "$hb" & wait $!; do
      kill -0 "$parent" 2> /dev/null || exit 0
      [ -f "$RELEASE_LOCK_STATE" ] || exit 0
      cur="$(cat "$RELEASE_LOCK_STATE")" next="$(_release_lock_commit "$msg")" || continue
      git push -q "$remote" "${next}:${RELEASE_LOCK_REF}" --force-with-lease="${RELEASE_LOCK_REF}:${cur}" > /dev/null 2>&1 \
        && printf '%s\n' "$next" > "$RELEASE_LOCK_STATE"
    done
  ) < /dev/null > /dev/null 2>&1 &
  RELEASE_LOCK_HB_PID=$!
  return 0
}

release_lock_release() {
  [ -n "$RELEASE_LOCK_STATE" ] || return 0
  local remote="${RELEASE_LOCK_REMOTE:-origin}" cur
  # Safe under set -e: this runs from EXIT traps, where a failing command would replace
  # the script's exit status.
  if [ -n "$RELEASE_LOCK_HB_PID" ]; then
    kill "$RELEASE_LOCK_HB_PID" 2> /dev/null || true
    wait "$RELEASE_LOCK_HB_PID" 2> /dev/null || true
  fi
  cur="$(cat "$RELEASE_LOCK_STATE" 2> /dev/null || true)"
  rm -f "$RELEASE_LOCK_STATE"
  if ! git push -q "$remote" --force-with-lease="${RELEASE_LOCK_REF}:${cur}" ":${RELEASE_LOCK_REF}" > /dev/null 2>&1; then
    # The heartbeat may have been stopped after its push landed but before it recorded
    # the new sha: a lock carrying our own message is still ours to drop.
    cur="$(timeout 30 git ls-remote "$remote" "$RELEASE_LOCK_REF" 2> /dev/null | cut -f1 || true)"
    if [ -n "$cur" ] && timeout 30 git fetch -q "$remote" "+${RELEASE_LOCK_REF}:refs/release-chain/lock" 2> /dev/null \
      && [ "$(git log -1 --format=%s refs/release-chain/lock)" = "$RELEASE_LOCK_MSG" ] \
      && git push -q "$remote" --force-with-lease="${RELEASE_LOCK_REF}:${cur}" ":${RELEASE_LOCK_REF}" > /dev/null 2>&1; then
      :
    elif [ -n "$cur" ]; then
      echo "release-lock: could not remove ${RELEASE_LOCK_REF} (it expires ${RELEASE_LOCK_STALE_MINUTES:-15} min after its last heartbeat)" >&2
    fi
  fi
  RELEASE_LOCK_STATE="" RELEASE_LOCK_HB_PID=""
  unset RELEASE_LOCK_HELD
  release_lock_recheck
  return 0
}

# release_lock_recheck: what merged while the lock was held may be due. Re-apply
# release:asap to main's newest merged PR when it is: an untagged release:asap PR, or
# RELEASE_EVERY (10) first-parent merges since origin's newest tag. Best effort, quiet;
# the label event starts the release workflow, which judges main's tip itself.
# Like release_lock_release, safe under set -e (it runs from EXIT traps).
release_lock_recheck() {
  [ "${RELEASE_LOCK_RECHECK:-on}" != off ] || return 0
  command -v gh > /dev/null 2>&1 || return 0
  local remote="${RELEASE_LOCK_REMOTE:-origin}" slug tag n due="" asap newest oid main
  slug="${RELEASE_LOCK_SLUG:-$(git remote get-url "$remote" 2> /dev/null | sed -n 's|.*github\.com[:/]\([^/]*/[^/.]*\)\(\.git\)\{0,1\}$|\1|p')}"
  [ -n "$slug" ] || return 0
  timeout 60 git fetch -q --prune "$remote" '+refs/heads/main:refs/release-chain/main' '+refs/tags/v*:refs/release-chain/origin-tags/v*' 2> /dev/null || return 0
  main="$(git rev-parse -q --verify refs/release-chain/main)" || return 0
  # Nothing merged while we held the lock: no run was coalesced behind us, and a cut
  # that failed must not re-start itself.
  [ "$main" != "$RELEASE_LOCK_MAIN" ] || return 0
  tag="$(git for-each-ref --sort=-v:refname --format='%(refname)' refs/release-chain/origin-tags/ | head -n 1)"
  [ -n "$tag" ] || return 0
  n="$(git rev-list --first-parent --count "$(git merge-base "$tag" "$main")..$main" 2> /dev/null || echo 0)"
  [ "$n" -gt 0 ] || return 0
  [ "$n" -lt "${RELEASE_EVERY:-10}" ] || due="$n merges since the newest tag"
  if [ -z "$due" ]; then
    for oid in $(timeout 60 gh pr list -R "$slug" --state merged --base main --label release:asap --limit 1000 --json mergeCommit --jq '.[].mergeCommit.oid' 2> /dev/null); do
      git merge-base --is-ancestor "$oid" "$main" 2> /dev/null && ! git merge-base --is-ancestor "$oid" "$tag" 2> /dev/null && { due="an untagged release:asap PR"; break; }
    done
  fi
  [ -n "$due" ] || return 0
  newest="$(timeout 60 gh pr list -R "$slug" --state merged --base main --limit 1 --json number --jq '.[0].number' 2> /dev/null || true)"
  [ -n "$newest" ] || return 0
  asap="$(timeout 30 gh pr view "$newest" -R "$slug" --json labels --jq '[.labels[].name] | index("release:asap") != null' 2> /dev/null || true)"
  # Removed first, so the add is a fresh `labeled` event that starts the workflow.
  [ "$asap" != true ] || timeout 30 gh pr edit "$newest" -R "$slug" --remove-label release:asap > /dev/null 2>&1 || true
  timeout 30 gh pr edit "$newest" -R "$slug" --add-label release:asap > /dev/null 2>&1 \
    && echo "release-lock: main has more to release ($due); labelled #$newest release:asap to start the next release" >&2
  return 0
}
