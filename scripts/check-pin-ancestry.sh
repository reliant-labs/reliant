#!/usr/bin/env bash
# Fail when the forge pin names a commit that is not on forge's main.
#
# ── Why this exists ──────────────────────────────────────────────────────
#
# Pre-launch we pin forge by COMMIT (docs/development/pinning.md). A
# cross-repo change normally lands like this: pin forge's PR-head commit, get
# this repo's CI green, merge both. If forge SQUASH-merges, that PR-head
# commit never reaches forge's main — main gets a new commit with the same
# tree — and this repo's main is left pinning a commit that only a PR ref or a
# soon-deleted branch keeps alive. It keeps building only while
# proxy.golang.org happens to have it cached; with GOPRIVATE (what
# pin-forge.sh and CI's forge install use) it already fails:
#     unknown revision 1b0f7f557663
# Not hypothetical: reliant main pinned forge 1b0f7f55 on 2026-09-25, after
# forge #235 was squash-merged as 83635df0.
#
# ── Why it FAILS, unlike pin-drift.sh ────────────────────────────────────
#
# pin-drift.sh warns, because an OLD pin is a legitimate choice. A DANGLING
# pin never is: nothing guarantees the commit survives, and the fix is always
# the same one command, which pins a commit on main.
#   behind main  -> warning (pin-drift.sh)
#   not on main  -> failure (here)
#
# ── How ──────────────────────────────────────────────────────────────────
#
# A blob-less fetch of forge's main into a throwaway bare repo: every commit
# and tree, no file contents (~2 MB, ~2 s). No auth — forge is public. Then
# `git merge-base --is-ancestor`. A pinned commit that is absent from that
# fetch is not on main by construction. GIT_NO_LAZY_FETCH stops the partial
# repo from quietly fetching a missing commit by sha (GitHub serves PR-ref
# commits), which would still give the right answer, only slower.
#
# A TAG pin passes when the tag's commit is on main. A tag cut on a side
# branch does not.
#
# Usage:
#   scripts/check-pin-ancestry.sh             # human-readable
#   scripts/check-pin-ancestry.sh --github    # also ::error annotations for CI
#   scripts/check-pin-ancestry.sh --root DIR  # check DIR/go.mod (self-test)
#
# PIN_ANCESTRY_FORGE_REMOTE overrides forge's remote; the self-test
# (scripts/test-pin-ancestry.sh) points it at a throwaway repo.
#
# Exit: 0 every pin is on main · 1 a pin is not · 2 the check could not run.

set -euo pipefail

GITHUB_MODE=0
ROOT=""
while [ $# -gt 0 ]; do
  case "$1" in
    --github) GITHUB_MODE=1 ;;
    --root) ROOT="${2:?--root needs a directory}"; shift ;;
    *) echo "usage: $0 [--github] [--root DIR]" >&2; exit 2 ;;
  esac
  shift
done
[ -n "${ROOT}" ] || ROOT="$(git rev-parse --show-toplevel)"
cd "${ROOT}"

FORGE_REMOTE="${PIN_ANCESTRY_FORGE_REMOTE:-https://github.com/reliant-labs/forge}"

export GIT_NO_LAZY_FETCH=1
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

FAILED=0
BROKEN=0

annotate() { [ "${GITHUB_MODE}" = "1" ] && echo "::error file=$1,title=$2::$3" || true; }

go_require() { # <go.mod> <module> -> version, or nothing
  [ -f "$1" ] || return 0
  sed -n "s|^[[:space:]]*\(require[[:space:]]\{1,\}\)\{0,1\}$2[[:space:]]\{1,\}\(v[^[:space:]]*\).*|\2|p" "$1" | head -1
}

# The git ref a declared pin names:
#   pseudo-version  v0.1.18-0.20260925050659-5925770089f4 -> 5925770089f4
#   tag             v0.1.17                                -> v0.1.17
# Every pseudo-version form Go writes ends -<14-digit time>-<12-hex sha>. A
# build suffix (+incompatible, +dirty) is dropped first.
pin_commitish() {
  local v="${1%%+*}"
  if printf '%s' "${v}" | grep -Eq '[.-][0-9]{14}-[0-9a-f]{12}$'; then echo "${v##*-}"; else echo "${v}"; fi
}
is_sha() { printf '%s' "$1" | grep -Eq '^[0-9a-f]{7,40}$'; }

# Fetch <remote>'s main once, blob-less, into WORK/<name>.git.
sibling_git() { # <name> <remote> -> git dir
  local dir="${WORK}/$1.git"
  if [ ! -f "${dir}/.fetched" ]; then
    rm -rf "${dir}"
    git init --quiet --bare "${dir}"
    git -C "${dir}" fetch --quiet --no-tags --filter=blob:none "$2" "+refs/heads/main:refs/heads/main" || return 1
    touch "${dir}/.fetched"
  fi
  echo "${dir}"
}

# check <file> <what> <name> <remote> <declared> <fix>
check() {
  local file="$1" what="$2" name="$3" remote="$4" declared="$5" fix="$6"
  local label="${file} ${what}" where="${remote#https://}@main"

  if [ -z "${declared}" ]; then
    echo "  FAIL  ${label}: no pin found — this check would otherwise pass vacuously"
    annotate "${file}" "No ${name} pin" "${label}: no ${name} pin found. If the declaration moved, update scripts/check-pin-ancestry.sh; do not delete the check."
    FAILED=1
    return
  fi

  local dir
  if ! dir="$(sibling_git "${name}" "${remote}")"; then
    echo "  ERROR ${label} ${declared}: could not fetch main from ${remote}"
    BROKEN=1
    return
  fi

  local ref commit=""
  ref="$(pin_commitish "${declared}")"
  if is_sha "${ref}"; then
    commit="$(git -C "${dir}" rev-parse --verify --quiet "${ref}^{commit}" || true)"
  else
    local listed
    if ! listed="$(git ls-remote --tags "${remote}" "refs/tags/${ref}")"; then
      echo "  ERROR ${label} ${declared}: could not list tags on ${remote}"
      BROKEN=1
      return
    fi
    if [ -z "${listed}" ]; then
      echo "  FAIL  ${label} ${declared}: ${remote#https://} has no tag ${ref}"
      annotate "${file}" "Unknown ${name} tag" "${label} ${declared}: ${remote#https://} has no tag ${ref}. Fix: ${fix}."
      FAILED=1
      return
    fi
    if ! git -C "${dir}" fetch --quiet --no-tags --filter=blob:none "${remote}" "+refs/tags/${ref}:refs/tags/${ref}"; then
      echo "  ERROR ${label} ${declared}: could not fetch tag ${ref} from ${remote}"
      BROKEN=1
      return
    fi
    commit="$(git -C "${dir}" rev-parse --verify --quiet "refs/tags/${ref}^{commit}" || true)"
  fi

  if [ -n "${commit}" ] && git -C "${dir}" merge-base --is-ancestor "${commit}" refs/heads/main; then
    echo "  OK    ${label} ${declared}: ${ref} is on ${where}"
    return
  fi

  echo "  FAIL  ${label} ${declared}: ${ref} is NOT on ${where}"
  echo "        Only a branch or PR ref keeps it alive (a squash-merged PR head?), or it is gone."
  echo "        Fix: ${fix}"
  annotate "${file}" "Dangling ${name} pin" "${label} ${declared} names ${ref}, which is not on ${where}. Only a branch or PR ref keeps it alive (typically a PR head that was squash-merged), so the pin can stop resolving at any time. Fix: ${fix}. See docs/development/pinning.md."
  FAILED=1
}

echo "pin ancestry (FAILS on a pin that is not on the sibling's main):"
check "go.mod" "github.com/reliant-labs/forge" forge "${FORGE_REMOTE}" \
  "$(go_require go.mod github.com/reliant-labs/forge)" \
  "'make pin-forge' (pins forge's current main)"

if [ "${FAILED}" = "1" ]; then
  exit 1
fi
if [ "${BROKEN}" = "1" ]; then
  echo "the check could not run — this is NOT a pass" >&2
  exit 2
fi
