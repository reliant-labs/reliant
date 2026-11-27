#!/usr/bin/env bash
# test-upload.sh — send a commit's recorded test runs to the control plane:
# `forge test report --commit <sha> --upload`, best effort.
#
#   scripts/test-upload.sh <sha>
#
# Two callers, each with the recorded runs already in .forge/tests/runs/:
#   - ci.yml's test-upload job, on main, after the release gate's parts: their
#     lanes (unit, frontend, integration, e2e; scripts/test-capture.sh) were
#     recorded in each part's private checkout and handed over as artifacts.
#   - test-upload-merge-queue.yml, after a merge group's `Merge queue` run: the
#     merge gate's one run (lanes short and web-affected, scripts/merge-gate.sh),
#     kept as an artifact. That workflow runs main's copy of this script, never
#     the group's.
# The Tests pages of the reliant project then show their results, coverage
# and flaky tests (Tests D3b).
#
# THE CREDENTIAL IS FORGE'S TO FIND. It always asks for the upload and lets
# forge pick the credential: FORGE_CONTROL_PLANE_TOKEN when set, else the job's
# GitHub Actions OIDC identity (the job needs `permissions: id-token: write`,
# which sets ACTIONS_ID_TOKEN_REQUEST_URL), which the control plane exchanges
# for a short-lived `tests:write` token. Both callers pass no token: they
# upload with OIDC alone. (The `tests-upload` environment still holds a
# `tests:write` token; passing it to the upload step again is the rollback.)
# When neither works, forge says why in one line on stderr.
#
# IT NEVER CHANGES A VERDICT, and it always exits 0: the gates already judged
# the lanes, and the upload is a copy of what they recorded.
#   - No recorded run: a warning, nothing sent.
#   - No credential, a failed upload, a control plane that refuses or does not
#     yet offer the OIDC exchange, or one that does not record test runs yet:
#     forge's own warning.
# The report itself (text: results, merged coverage, diff coverage) is printed
# either way, so the job's log shows what the lanes measured.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 0

sha=${1:-}
if [ -z "$sha" ]; then
  echo "usage: $0 <sha>" >&2
  exit 0
fi
warn() {
  if [ -n "${GITHUB_ACTIONS:-}" ]; then echo "::warning title=Test results not uploaded::$*"; else echo "test-upload: $*" >&2; fi
}

if ! ls .forge/tests/runs/*/run.json >/dev/null 2>&1; then
  warn "no recorded test run under .forge/tests/runs/ for ${sha:0:12}; nothing to report"
  exit 0
fi

# Which credential forge will find first (cloud.ResolveCredentialFor), named
# so the job's log says which path an upload took. Never the value, and never
# a reason to skip: forge is asked either way, and says why when it has none.
if [ -n "${FORGE_CONTROL_PLANE_TOKEN:-}" ]; then
  echo "test-upload: credential: FORGE_CONTROL_PLANE_TOKEN"
elif [ -n "${ACTIONS_ID_TOKEN_REQUEST_URL:-}" ]; then
  echo "test-upload: credential: this job's GitHub Actions OIDC identity, exchanged for tests:write"
else
  echo "test-upload: credential: no FORGE_CONTROL_PLANE_TOKEN and no GitHub Actions OIDC identity (id-token: write); forge looks for a stored login"
fi

scripts/forge.sh test report --commit "$sha" --upload || echo "test-upload: forge test report exited $? (no verdict changes)"
exit 0
