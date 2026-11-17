#!/usr/bin/env bash
# check-changelog-untouched.sh — fail a pull request whose diff touches
# CHANGELOG.md.
#
#   scripts/check-changelog-untouched.sh [BASE]      BASE defaults to HEAD^1
#
# CHANGELOG.md is written only at release time, by scripts/release-forge.sh,
# from the merged PR titles. A pull request that edits it is the conflict this
# policy removes: 203 of 319 commits on main used to append under [Unreleased],
# which queued every merge behind a textual conflict. So the way to get a
# change into the changelog is a good PR title (`feat(x): ...`, `fix(y): ...`,
# a `!` or a `BREAKING CHANGE:` footer for a breaking one), not an edit.
#
# Allowed:
#   - a release branch: GITHUB_HEAD_REF (or --branch) matching release/*;
#   - the release commit itself: a commit in BASE..HEAD whose subject is
#     `chore: release vX.Y.Z`, which is what release-forge.sh writes.
#
# On a pull request actions/checkout checks out the merge commit, whose first
# parent is the base branch's tip, so with fetch-depth: 2 `HEAD^1..HEAD` is what
# the pull request changes. Skipped in the merge queue by ci.yml: the entry has
# no head branch name to test, and the pull request was checked when it opened.
set -euo pipefail

branch="${GITHUB_HEAD_REF:-}"
if [ "${1:-}" = "--branch" ]; then
  [ $# -ge 2 ] || { echo "usage: $0 [--branch NAME] [BASE]" >&2; exit 2; }
  branch="$2"
  shift 2
fi
base="${1:-HEAD^1}"

if ! git rev-parse -q --verify "$base" >/dev/null; then
  echo "error: cannot resolve base '$base'; the checkout needs fetch-depth: 2" >&2
  exit 2
fi

touched="$(git diff --name-only "$base" HEAD -- CHANGELOG.md)"
if [ -z "$touched" ]; then
  echo "ok: CHANGELOG.md is untouched"
  exit 0
fi

case "$branch" in
  release/*)
    echo "ok: CHANGELOG.md changed on release branch '$branch'"
    exit 0 ;;
esac

if git log --format=%s "$base..HEAD" | grep -Eq '^chore: release v[0-9]+\.[0-9]+\.[0-9]+'; then
  echo "ok: CHANGELOG.md changed by a release commit"
  exit 0
fi

cat >&2 <<'EOF'
error: this change edits CHANGELOG.md, which only the release script writes.

CHANGELOG.md is generated at release time by scripts/release-forge.sh from the
merged pull request titles, so a PR that edits it only creates merge conflicts.

  - Revert your CHANGELOG.md edit:  git restore --source=origin/main -- CHANGELOG.md
  - Say what changed in the PR TITLE, in conventional-commit form:
        feat(scope): what a user can now do      -> Added
        fix(scope): what no longer breaks         -> Fixed
        anything else (refactor, docs, ...)       -> Changed
    A breaking change takes a `!` (feat(scope)!: ...) or a `BREAKING CHANGE:`
    footer, and shows up under "Breaking changes".

Release branches (release/*) and the `chore: release vX.Y.Z` commit are exempt.
EOF
exit 1
