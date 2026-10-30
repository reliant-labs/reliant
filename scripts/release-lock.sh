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
# A run that was DUE but coalesced records that on origin (refs/release-lock/coalesced).
# When the holder releases the lock it acts on that record (release_lock_recheck): it
# re-applies release:asap to main's newest merged PR, which starts the release workflow
# again, judged at main's tip. So a coalesced run is never lost, whenever it was asked
# for (a pending asap merge whose run lands mid-release included), and a failed cut
# nobody asked to repeat never re-starts itself.
#
# The lock FAILS CLOSED. A heartbeat that finds the lock on origin is no longer ours, or
# that cannot renew it for RELEASE_LOCK_RENEW_DEADLINE_SECS (default: 3 min before it
# would go stale), marks it LOST and stops the release: TERM to the holder's process
# tree. And every publication step (a tag push, a prod deploy) first calls
# release_lock_assert, which proves on origin that the lock is still ours.
#
#   release_lock_acquire <version>   take it, or print who holds it and return 1
#   release_lock_assert              0 only if the lock is ours on origin right now:
#                                    call it before every irreversible publication
#   release_lock_release             drop it if still ours, stop the heartbeat, re-check main
#
# A script that re-runs itself (cut-release.sh --cut-recorded) exports
# RELEASE_LOCK_HELD=<state dir> to its child, which then neither takes nor drops it, and
# asserts against the parent's state.
# RELEASE_LOCK=off skips it (fixtures with no remote only); RELEASE_LOCK_RECHECK=off skips
# the re-check (tests).
RELEASE_LOCK_REF="refs/release-lock/release" RELEASE_LOCK_STATE="" RELEASE_LOCK_HB_PID=""
RELEASE_LOCK_MSG=""
# Where the release workflow records a request it coalesced into a live lock.
RELEASE_LOCK_COALESCED_REF="refs/release-lock/coalesced"

# How long the holder may go without a confirmed renewal before it stops: comfortably
# inside the stale window, so no other cutter can have replaced a lock we still act on.
_release_lock_deadline() {
  local stale=$((${RELEASE_LOCK_STALE_MINUTES:-15} * 60)) hb="${RELEASE_LOCK_HEARTBEAT_SECS:-60}" margin
  margin=$((3 * hb > 180 ? 3 * hb : 180))
  echo "${RELEASE_LOCK_RENEW_DEADLINE_SECS:-$((stale - margin > hb ? stale - margin : hb))}"
}

# The descendants of <pid>, never <self> (the heartbeat) or anything below it.
_release_lock_descendants() { # <pid> <self>
  local c
  for c in $(pgrep -P "$1" 2> /dev/null); do
    [ "$c" = "$2" ] || { echo "$c"; _release_lock_descendants "$c" "$2"; }
  done
}

# Stop the holder's release: TERM the holder FIRST (so it runs no further command, and
# its EXIT trap reports the loss), then everything it had started, from a snapshot taken
# before (a dead holder's children are re-parented and could no longer be found).
_release_lock_stop() { # <holder> <self>
  local tree p
  trap '' TERM # the holder's EXIT trap stops this heartbeat; finish the stop first
  tree="$(_release_lock_descendants "$1" "$2")"
  kill -TERM "$1" 2> /dev/null || true
  for p in $tree; do kill -TERM "$p" 2> /dev/null || true; done
}

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

# _release_lock_read -> "<sha> <committer-epoch> <owner line>" of origin's lock, nothing
# when there is none; returns 1 when origin cannot be asked. The commit is fetched into a
# ref private to this call, so what is judged (age, owner) and the sha a caller leases
# on are one immutable commit: never a shared scratch ref another process in this
# checkout, or a linked worktree, may have moved in between.
_release_lock_read() { # [ref, default the lock]
  local remote="${RELEASE_LOCK_REMOTE:-origin}" ref="${1:-$RELEASE_LOCK_REF}" tmp line ls
  tmp="refs/release-chain/lock-read/$$-${RANDOM}${RANDOM}"
  if ! timeout 30 git fetch -q --no-write-fetch-head "$remote" "+${ref}:${tmp}" 2> /dev/null; then
    # No such ref on origin is a free lock; anything else is not knowing.
    ls="$(timeout 30 git ls-remote "$remote" "$ref" 2> /dev/null)" || return 1
    [ -z "$ls" ] || return 1
    return 0
  fi
  line="$(git log -1 --format='%H %ct %s' "$tmp" 2> /dev/null)" || line=""
  git update-ref -d "$tmp" 2> /dev/null || true
  [ -n "$line" ] || return 1
  printf '%s\n' "$line"
}

# release_lock_status -> "free" | "live <age-seconds> <sha> <owner line>"
# | "stale <age> <sha> <owner>" | "unknown" (origin could not be asked). Read-only; the
# workflow uses the same rule. A caller that acts on it leases on the <sha> it names.
release_lock_status() {
  local stale_m="${RELEASE_LOCK_STALE_MINUTES:-15}" out sha ct owner age
  out="$(_release_lock_read)" || { echo unknown; return 0; }
  [ -n "$out" ] || { echo free; return 0; }
  read -r sha ct owner <<< "$out"
  age=$(($(date +%s) - ct))
  if [ "$age" -ge $((stale_m * 60)) ]; then echo "stale $age $sha $owner"; else echo "live $age $sha $owner"; fi
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
        # Lease on the exact commit judged stale: if anyone replaced it since, this fails.
        old="$(echo "$st" | cut -d' ' -f3)"
        if git push -q "$remote" "${sha}:${RELEASE_LOCK_REF}" --force-with-lease="${RELEASE_LOCK_REF}:${old}" > /dev/null 2>&1; then
          echo "release-lock: replaced a stale lock (no heartbeat for $(($(echo "$st" | cut -d' ' -f2) / 60)) min: $(echo "$st" | cut -d' ' -f4-))" >&2
        else
          echo "release-lock: another cutter took the stale lock first; refusing" >&2; return 1
        fi ;;
      live\ *)
        echo "release-lock: a release of this repository is already in progress: $(echo "$st" | cut -d' ' -f4-) (heartbeat $(echo "$st" | cut -d' ' -f2)s ago)." >&2
        echo "  Wait for it; it re-checks main when it finishes. If its holder is dead, the lock expires ${RELEASE_LOCK_STALE_MINUTES:-15} min after its last heartbeat." >&2
        return 1 ;;
      *)
        echo "release-lock: cannot take ${RELEASE_LOCK_REF} on ${remote} (no push access, or the network); refusing to release unlocked" >&2
        return 1 ;;
    esac
  fi
  RELEASE_LOCK_STATE="$(mktemp -d)" || return 1
  printf '%s\n' "$sha" > "$RELEASE_LOCK_STATE/sha" && date +%s > "$RELEASE_LOCK_STATE/ok" || return 1
  RELEASE_LOCK_MSG="$msg"
  export RELEASE_LOCK_HELD="$RELEASE_LOCK_STATE"
  # The heartbeat: replace our own lock commit (CAS on the previous one) until released,
  # or until the holder is gone; fail closed when it cannot (see the header). Detached
  # from every output, so it never holds a pipe. TERM ends it between commands, never
  # inside a push (bash runs a trap only after the foreground child), and at once while
  # it sleeps (wait is interruptible).
  local holder="${BASHPID:-$$}"
  (
    trap 'exit 0' TERM
    # Hold no descriptor of the holder's beyond 0-2 (bash keeps copies of redirected
    # ones, 10 and up): a pipe the heartbeat kept open would keep the holder's reader
    # waiting after the holder itself is gone.
    for fd in $(ls "/proc/${BASHPID:-self}/fd" 2> /dev/null || ls /dev/fd 2> /dev/null); do
      [ "$fd" -gt 2 ] 2> /dev/null && eval "exec ${fd}>&-" 2> /dev/null
    done
    st="$RELEASE_LOCK_STATE" hb="${RELEASE_LOCK_HEARTBEAT_SECS:-60}" t="${RELEASE_LOCK_GIT_TIMEOUT:-30}"
    deadline="$(_release_lock_deadline)" self="${BASHPID:-$(sh -c 'echo "$PPID"')}"
    lost() { [ -e "$st/lost" ] || printf '%s\n' "$1" > "$st/lost"; }
    # Wait one heartbeat, looking every few seconds for a holder that is gone.
    beat() {
      local left="$hb" step
      while [ "$left" -gt 0 ]; do
        step=$((left < 5 ? left : 5)) left=$((left - step))
        sleep "$step" & wait $!
        kill -0 "$holder" 2> /dev/null && [ -d "$st" ] || return 1
      done
    }
    while beat; do
      [ ! -e "$st/lost" ] || exit 0
      cur="$(cat "$st/sha")" next=""
      if next="$(_release_lock_commit "$msg")" \
        && timeout "$t" git push -q "$remote" "${next}:${RELEASE_LOCK_REF}" --force-with-lease="${RELEASE_LOCK_REF}:${cur}" > /dev/null 2>&1; then
        printf '%s\n' "$next" > "$st/sha" && date +%s > "$st/ok"
        continue
      fi
      # Not renewed. Ask origin whose lock it is: a push that landed but timed out here is
      # still ours (every lock commit is distinct); any other sha, or none, is not.
      if have="$(timeout "$t" git ls-remote "$remote" "$RELEASE_LOCK_REF" 2> /dev/null)"; then
        have="${have%%[[:space:]]*}"
        if [ -n "$next" ] && [ "$have" = "$next" ]; then
          printf '%s\n' "$next" > "$st/sha" && date +%s > "$st/ok"
          continue
        fi
        [ "$have" = "$cur" ] || lost "the lock on origin is no longer ours (it is ${have:-gone})"
      fi
      [ $(($(date +%s) - $(cat "$st/ok"))) -lt "$deadline" ] || lost "it could not be renewed for ${deadline}s (origin unreachable?)"
      if [ -e "$st/lost" ]; then
        _release_lock_stop "$holder" "$self"
        exit 0
      fi
    done
  ) < /dev/null > /dev/null 2>&1 &
  RELEASE_LOCK_HB_PID=$!
  return 0
}

# release_lock_assert: prove on origin, now, that this release still holds the lock.
# Fails closed: lost, not renewed in time, origin unreachable or another sha all refuse.
release_lock_assert() {
  [ "${RELEASE_LOCK:-on}" != off ] || return 0
  local st="${RELEASE_LOCK_STATE:-${RELEASE_LOCK_HELD:-}}" remote="${RELEASE_LOCK_REMOTE:-origin}" before after have deadline
  if [ -z "$st" ] || [ ! -d "$st" ]; then
    echo "release-lock: this release does not hold ${RELEASE_LOCK_REF}; refusing to publish" >&2; return 1
  fi
  if [ -e "$st/lost" ]; then
    echo "release-lock: this release lost the release lock ($(cat "$st/lost" 2> /dev/null)); refusing to publish" >&2; return 1
  fi
  deadline="$(_release_lock_deadline)"
  if [ $(($(date +%s) - $(cat "$st/ok" 2> /dev/null || echo 0))) -ge "$deadline" ]; then
    echo "release-lock: the release lock was not renewed for ${deadline}s; refusing to publish" >&2; return 1
  fi
  # The heartbeat may renew while we ask: our sha before or after the read is ours.
  before="$(cat "$st/sha" 2> /dev/null || true)"
  if ! have="$(timeout "${RELEASE_LOCK_GIT_TIMEOUT:-30}" git ls-remote "$remote" "$RELEASE_LOCK_REF" 2> /dev/null)"; then
    echo "release-lock: cannot confirm on ${remote} that the release lock is still ours; refusing to publish" >&2; return 1
  fi
  have="${have%%[[:space:]]*}" after="$(cat "$st/sha" 2> /dev/null || true)"
  if [ -z "$have" ] || { [ "$have" != "$before" ] && [ "$have" != "$after" ]; }; then
    echo "release-lock: the release lock on ${remote} is no longer ours (it is ${have:-gone}); refusing to publish" >&2; return 1
  fi
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
  local st="$RELEASE_LOCK_STATE" lost=""
  [ ! -e "$st/lost" ] || lost="$(cat "$st/lost" 2> /dev/null || echo '?')"
  cur="$(cat "$st/sha" 2> /dev/null || true)"
  rm -rf "$st"
  if [ -n "$lost" ]; then
    # Whatever holds the lock now is someone else's release: leave it, and do not re-check
    # main on its behalf.
    echo "release-lock: this release LOST ${RELEASE_LOCK_REF} (${lost}) and was stopped: nothing was published after that." >&2
    RELEASE_LOCK_STATE="" RELEASE_LOCK_HB_PID=""
    unset RELEASE_LOCK_HELD
    return 0
  fi
  if ! git push -q "$remote" --force-with-lease="${RELEASE_LOCK_REF}:${cur}" ":${RELEASE_LOCK_REF}" > /dev/null 2>&1; then
    # The heartbeat may have been stopped after its push landed but before it recorded
    # the new sha: a lock carrying our own message is still ours to drop.
    # Judged and leased on one commit (_release_lock_read), so a lock someone else took
    # since is never dropped.
    local out="" owner=""
    out="$(_release_lock_read || true)" cur=""
    [ -z "$out" ] || read -r cur _ owner <<< "$out"
    if [ -n "$cur" ] && [ "$owner" = "$RELEASE_LOCK_MSG" ] \
      && git push -q "$remote" --force-with-lease="${RELEASE_LOCK_REF}:${cur}" ":${RELEASE_LOCK_REF}" > /dev/null 2>&1; then
      :
    elif [ -n "$out" ]; then
      echo "release-lock: could not remove ${RELEASE_LOCK_REF} (it expires ${RELEASE_LOCK_STALE_MINUTES:-15} min after its last heartbeat)" >&2
    fi
  fi
  RELEASE_LOCK_STATE="" RELEASE_LOCK_HB_PID=""
  unset RELEASE_LOCK_HELD
  release_lock_recheck
  return 0
}

# release_lock_recheck: a release run that came due while this lock was held was
# coalesced into it, and recorded that on origin (refs/release-lock/coalesced: the release
# workflow's coalesce node). Act on it: consume it (CAS) and re-apply release:asap to
# main's newest merged PR, which starts the release workflow again, judged at main's tip
# (a no-op when this release already shipped everything). Nothing recorded, nothing to
# do: a failed cut never re-starts itself, and a hand cut with no workflow behind it is
# left alone. Best effort; a request this misses stays on origin for the next release.
# Like release_lock_release, safe under set -e (it runs from EXIT traps).
release_lock_recheck() {
  [ "${RELEASE_LOCK_RECHECK:-on}" != off ] || return 0
  command -v gh > /dev/null 2>&1 || return 0
  local remote="${RELEASE_LOCK_REMOTE:-origin}" slug out sha why asap newest
  slug="${RELEASE_LOCK_SLUG:-$(git remote get-url "$remote" 2> /dev/null | sed -n 's|.*github\.com[:/]\([^/]*/[^/.]*\)\(\.git\)\{0,1\}$|\1|p')}"
  [ -n "$slug" ] || return 0
  out="$(_release_lock_read "$RELEASE_LOCK_COALESCED_REF" || true)"
  [ -n "$out" ] || return 0
  read -r sha _ why <<< "$out"
  # Consumed by exactly one releaser: a newer request (another sha) stays for the next.
  timeout 30 git push -q "$remote" --force-with-lease="${RELEASE_LOCK_COALESCED_REF}:${sha}" ":${RELEASE_LOCK_COALESCED_REF}" > /dev/null 2>&1 || return 0
  newest="$(timeout 60 gh pr list -R "$slug" --state merged --base main --limit 1 --json number --jq '.[0].number' 2> /dev/null || true)"
  if [ -z "$newest" ]; then
    echo "release-lock: ${why}; no merged PR was found to re-label: add release:asap to one" >&2
    return 0
  fi
  asap="$(timeout 30 gh pr view "$newest" -R "$slug" --json labels --jq '[.labels[].name] | index("release:asap") != null' 2> /dev/null || true)"
  # Removed first, so the add is a fresh `labeled` event that starts the workflow.
  [ "$asap" != true ] || timeout 30 gh pr edit "$newest" -R "$slug" --remove-label release:asap > /dev/null 2>&1 || true
  if timeout 30 gh pr edit "$newest" -R "$slug" --add-label release:asap > /dev/null 2>&1; then
    echo "release-lock: ${why}; labelled #$newest release:asap to start that release" >&2
  else
    echo "release-lock: ${why}; labelling #$newest release:asap failed: add it by hand" >&2
  fi
  return 0
}
