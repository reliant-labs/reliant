#!/usr/bin/env bash
# Report how far behind the sibling's main this repo's forge pin has fallen.
#
# ── Why this exists ──────────────────────────────────────────────────────
#
# A commit pin has one real weakness, and this is it: there is no version
# number to notice is stale. `v0.1.11` next to a `v0.1.14` release is
# obviously behind at a glance; `v0.1.15-0.20260910160302-c195f15386be` looks
# equally current whether it is one commit old or ninety. Nothing decays
# visibly, so nothing prompts a bump.
#
# ── Why it WARNS and never fails ─────────────────────────────────────────
#
# A deliberately older pin is legitimate — holding forge steady while
# investigating a regression is a normal thing to do, and a check that failed
# CI for it would be one whose only fix is to bypass it. A check people
# routinely bypass has already stopped being a check. So: say something,
# block nothing. This is the same posture as setup-forge's CLI/pkg divergence
# warning, for the same reason.
#
# Exit code is 0 unless the check itself could not run.
#
# Usage:
#   scripts/pin-drift.sh            # human-readable
#   scripts/pin-drift.sh --github   # also emit ::warning for CI annotations

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

GITHUB_MODE=0
[ "${1:-}" = "--github" ] && GITHUB_MODE=1

# The drift threshold at which we start nagging. Small enough to be useful,
# large enough that a normal same-day pin does not trip it.
THRESHOLD="${PIN_DRIFT_THRESHOLD:-10}"

pinned_version() {
  sed -n "s|^[[:space:]]*$1[[:space:]]\{1,\}\(v[^[:space:]]*\).*|\1|p" go.mod | head -1
}

# A pin is one of two shapes and the compare API accepts both as a base:
#   tag              v0.1.14
#   pseudo-version   v0.1.15-0.20260910160302-c195f15386be  (sha = last field)
# Reducing both to "something git can resolve" keeps this working unchanged
# across the launch switch back to tags, which is the point — a drift check
# that only understands one mode has to be rewritten at the worst moment.
pin_ref() {
  case "$1" in
    *-*-*) echo "${1##*-}" ;;
    *)     echo "$1" ;;
  esac
}

report() {
  local label="$1" repo="$2" version="$3"

  if [ -z "${version}" ]; then
    echo "  ${label}: not pinned in go.mod — skipping"
    return
  fi

  local ref; ref="$(pin_ref "${version}")"
  local behind
  # ahead_by counts commits main has that the pin does not — i.e. exactly how
  # far behind the pin is. Needs gh auth; these are private repos.
  behind="$(gh api "repos/${repo}/compare/${ref}...main" --jq '.ahead_by' 2>/dev/null || true)"

  if [ -z "${behind}" ]; then
    echo "  ${label}: ${version}"
    echo "      could not compare against ${repo}@main (gh auth? unreachable commit?) — skipped"
    return
  fi

  local shape="tag"
  case "${version}" in *-*-*) shape="commit" ;; esac

  if [ "${behind}" -eq 0 ]; then
    echo "  ${label}: ${version} [${shape}] — up to date with ${repo}@main"
  elif [ "${behind}" -lt "${THRESHOLD}" ]; then
    echo "  ${label}: ${version} [${shape}] — ${behind} commit(s) behind ${repo}@main"
  else
    echo "  ${label}: ${version} [${shape}] — ${behind} commits behind ${repo}@main  (>= ${THRESHOLD})"
    if [ "${GITHUB_MODE}" = "1" ]; then
      echo "::warning file=go.mod::${label} is ${behind} commits behind ${repo}@main (pinned ${version}). Pre-launch we pin by commit, so nothing about this version string looks stale on its own. Run 'make pin-forge' to move it, or ignore this if the older pin is deliberate. See docs/pinning.md."
    fi
  fi
}

echo "pin drift (warn-only; a deliberately older pin is fine):"
report "forge    " "reliant-labs/forge" "$(pinned_version github.com/reliant-labs/forge)"
report "forge/pkg" "reliant-labs/forge" "$(pinned_version github.com/reliant-labs/forge/pkg)"
