# shellcheck shell=bash
# release-lock.sh — one release of a version at a time, across machines. Sourced by the
# release scripts (forge scripts/release-forge.sh, reliant scripts/release-tag.sh,
# control-plane scripts/cut-release.sh; the same file in each repo).
#
# The lock is a ref on origin, refs/release-lock/<version>, pushed with
# --force-with-lease=<ref>: ("must not exist"), which GitHub applies atomically: of two
# cutters, exactly one creates it and the other is refused. So a hand cut and the
# release workflow cannot both release one version. It is not a branch (no ruleset, no
# PR, invisible in the branch list) and points at an empty-tree commit whose message
# names the holder and whose date is when it was taken.
#
#   release_lock_acquire <version>   take it, or print who holds it and return 1
#   release_lock_release             drop it if it is still ours (call from the EXIT trap)
#
# A lock older than RELEASE_LOCK_STALE_HOURS (default 6; a cut with its promotion takes
# under 2) is a holder that died: it is replaced, compare-and-swap on its sha, so two
# stealers cannot both win. RELEASE_LOCK=off skips it (fixtures with no remote only).
# A script that re-runs itself (cut-release.sh --cut-recorded) exports
# RELEASE_LOCK_HELD=<version> to its child, which then neither takes nor drops it.
RELEASE_LOCK_REF="" RELEASE_LOCK_SHA=""

release_lock_acquire() {
  local version="$1" remote="${RELEASE_LOCK_REMOTE:-origin}" stale_h="${RELEASE_LOCK_STALE_HOURS:-6}"
  [ "${RELEASE_LOCK:-on}" != off ] || return 0
  [ "${RELEASE_LOCK_HELD:-}" != "$version" ] || return 0
  local ref="refs/release-lock/${version}" tree msg sha old age
  tree="$(git mktree < /dev/null)" || return 1
  msg="release lock ${version}: $(id -un 2> /dev/null || echo ?)@$(hostname 2> /dev/null || echo ?) pid $$ ${RELIANT_RUN_ID:+run ${RELIANT_RUN_ID} }$(basename "${0}")"
  sha="$(printf '%s\n' "$msg" | GIT_AUTHOR_NAME=release-lock GIT_AUTHOR_EMAIL=release-lock@invalid \
    GIT_COMMITTER_NAME=release-lock GIT_COMMITTER_EMAIL=release-lock@invalid git commit-tree "$tree")" || return 1
  if git push -q "$remote" "${sha}:${ref}" --force-with-lease="${ref}:" > /dev/null 2>&1; then
    RELEASE_LOCK_REF="$ref" RELEASE_LOCK_SHA="$sha"
    return 0
  fi
  old="$(git ls-remote "$remote" "$ref" 2> /dev/null | awk '{print $1}')"
  if [ -z "$old" ]; then
    echo "release-lock: cannot push ${ref} to ${remote} (no push access, or the network); refusing to release unlocked" >&2
    return 1
  fi
  git fetch -q "$remote" "$ref" 2> /dev/null || true
  age=$(($(date +%s) - $(git log -1 --format=%ct "$old" 2> /dev/null || date +%s)))
  if [ "$age" -ge $((stale_h * 3600)) ] \
    && git push -q "$remote" "${sha}:${ref}" --force-with-lease="${ref}:${old}" > /dev/null 2>&1; then
    echo "release-lock: replaced a stale lock ($((age / 3600))h old: $(git log -1 --format=%s "$old" 2> /dev/null))" >&2
    RELEASE_LOCK_REF="$ref" RELEASE_LOCK_SHA="$sha"
    return 0
  fi
  echo "release-lock: a release of ${version} is already in progress: $(git log -1 --format=%s "$old" 2> /dev/null || echo "lock ${old:0:12}"), taken $((age / 60)) min ago." >&2
  echo "  Wait for it. If that holder is dead (it expires after ${stale_h}h), remove it: git push ${remote} --force-with-lease=${ref}:${old} :${ref}" >&2
  return 1
}

release_lock_release() {
  [ -n "$RELEASE_LOCK_REF" ] || return 0
  git push -q "${RELEASE_LOCK_REMOTE:-origin}" --force-with-lease="${RELEASE_LOCK_REF}:${RELEASE_LOCK_SHA}" ":${RELEASE_LOCK_REF}" > /dev/null 2>&1 \
    || echo "release-lock: could not remove ${RELEASE_LOCK_REF} (it expires after ${RELEASE_LOCK_STALE_HOURS:-6}h)" >&2
  RELEASE_LOCK_REF="" RELEASE_LOCK_SHA=""
  return 0
}
