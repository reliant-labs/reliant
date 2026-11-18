#!/usr/bin/env bash
#
# Confirm the npm registry actually SERVES a version, after `npm publish` has
# reported success.
#
# WHY THIS IS A SEPARATE SCRIPT. It was inline YAML in
# .github/workflows/release-web-runtime.yml, where it could not be tested —
# and it was wrong in a way only a test would have caught (see the window
# below). A release-day guard that has never been executed against a failing
# registry is a guess about what the registry does.
#
# WHAT IT GUARDS. `npm publish` exiting 0 means the registry ACCEPTED the
# upload, not that the version is fetchable. Those came apart for real: the
# first 0.3.1 publish was authenticated, packed, signed and written to the
# sigstore transparency log before the registry refused it, and every upstream
# signal reported success. A tag that exists while the registry serves nothing
# is invisible until a scaffolded project fails to install days later.
#
# THE WINDOW, AND WHY 60s WAS WRONG. The original loop waited six attempts at
# 10s — ~60s total — on the reasoning that propagation is near-instant. It is
# not. 0.3.2 published successfully and was not served for several minutes,
# so the guard failed a release that had in fact worked, and the error text
# told the operator the artifact was missing when it was merely late. A
# false red here is expensive twice over: it burns a release slot, and it
# trains people to disbelieve the one check that catches the real failure.
#
# So the window is now ~10 minutes with exponential backoff (capped), which is
# far longer than any propagation delay observed and still bounded. Backoff
# rather than a fixed interval because the common case resolves in seconds and
# should stay fast, while the slow case should not hammer the registry 60
# times.
#
# THE TWO OUTCOMES ARE NOT THE SAME FAILURE, and the message must say which:
#
#   published but not yet served — the publish step itself reported success,
#     so the artifact exists and this is propagation taking longer than the
#     window. Re-check before re-cutting anything; do NOT re-publish, the
#     version is immutable and a second attempt will 403.
#
#   not published — no success signal from the publish step. The tag exists
#     without an artifact, which is the drift this check was written for.
#
# The caller distinguishes them by passing --publish-reported-success (the
# workflow sets it from the publish step's own outcome). Without it, a
# timeout is reported as the more serious "not published" case, which is the
# right default: assume the worse state when the evidence is absent.
#
# TESTING SEAM: NPM_VIEW_CMD overrides the command used to ask the registry,
# and NPM_VERIFY_MAX_SECONDS / NPM_VERIFY_FIRST_DELAY shrink the window so a
# test can exercise the timeout path in milliseconds rather than minutes.
# Nothing else about the logic changes between CI and test.

set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: verify-npm-published.sh --name <pkg> --version <ver> [--publish-reported-success]

Polls the npm registry until it serves <pkg>@<ver>, or the window elapses.

  --name                       package name, e.g. @reliantlabs/forge-web-runtime
  --version                    exact version, e.g. 0.3.2
  --publish-reported-success   the publish step exited 0; changes a timeout
                               from "not published" to "published but not yet
                               served"

Environment (testing seams):
  NPM_VIEW_CMD            command to probe with (default: npm view)
  NPM_VERIFY_MAX_SECONDS  total window in seconds (default: 600)
  NPM_VERIFY_FIRST_DELAY  first backoff delay in seconds (default: 5)
  NPM_VERIFY_MAX_DELAY    cap on any single sleep, seconds (default: 30)
EOF
  exit 2
}

NAME=""
VERSION=""
PUBLISH_OK=0

while [ $# -gt 0 ]; do
  case "$1" in
    --name) NAME="${2:-}"; shift 2 ;;
    --version) VERSION="${2:-}"; shift 2 ;;
    --publish-reported-success) PUBLISH_OK=1; shift ;;
    -h|--help) usage ;;
    *) echo "unknown argument: $1" >&2; usage ;;
  esac
done

[ -n "$NAME" ] || { echo "--name is required" >&2; usage; }
[ -n "$VERSION" ] || { echo "--version is required" >&2; usage; }

VIEW_CMD="${NPM_VIEW_CMD:-npm view}"
MAX_SECONDS="${NPM_VERIFY_MAX_SECONDS:-600}"
DELAY="${NPM_VERIFY_FIRST_DELAY:-5}"
# Cap a single sleep so the backoff cannot overshoot the window in one jump
# and so the last stretch still polls at a useful cadence.
MAX_DELAY="${NPM_VERIFY_MAX_DELAY:-30}"

echo "confirming ${NAME}@${VERSION} is fetchable (up to ${MAX_SECONDS}s)..."

elapsed=0
attempt=0
while :; do
  attempt=$((attempt + 1))
  if $VIEW_CMD "${NAME}@${VERSION}" version >/dev/null 2>&1; then
    echo "registry serves ${NAME}@${VERSION} (after ${elapsed}s, ${attempt} attempt(s))"
    $VIEW_CMD "${NAME}@${VERSION}" dist.integrity || true
    exit 0
  fi

  if [ "$elapsed" -ge "$MAX_SECONDS" ]; then
    break
  fi

  # Never sleep past the end of the window.
  remaining=$((MAX_SECONDS - elapsed))
  if [ "$DELAY" -gt "$remaining" ]; then
    DELAY="$remaining"
  fi
  echo "  attempt ${attempt}: not yet visible, waiting ${DELAY}s (${elapsed}s elapsed)"
  sleep "$DELAY"
  elapsed=$((elapsed + DELAY))

  DELAY=$((DELAY * 2))
  if [ "$DELAY" -gt "$MAX_DELAY" ]; then
    DELAY="$MAX_DELAY"
  fi
done

if [ "$PUBLISH_OK" -eq 1 ]; then
  echo "::error::${NAME}@${VERSION} was PUBLISHED BUT IS NOT YET SERVED. npm publish reported success, so the artifact exists and the registry is still propagating it — this is not the missing-artifact case. Do NOT re-publish: the version is immutable and a second attempt will be refused. Re-run this check, or confirm by hand with 'npm view ${NAME}@${VERSION} version', before cutting anything that depends on it."
else
  echo "::error::${NAME}@${VERSION} is NOT PUBLISHED. The registry does not serve it and the publish step did not report success, so the tag now exists without a consumable artifact — exactly the drift this check guards. Investigate before cutting anything that depends on it."
fi
exit 1
