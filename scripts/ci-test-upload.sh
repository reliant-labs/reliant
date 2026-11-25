#!/usr/bin/env bash
# Send a commit's recorded test runs to the control plane:
# `forge test report --commit <sha> --upload`, best effort.
#
#   scripts/ci-test-upload.sh <sha>
#
# test-upload.yml runs it when a CI run of a push to main completes. That
# run's jobs recorded their lanes through `forge test capture`
# (scripts/ci-test-shard.sh, ci-test-full-shard.sh, ci-changed-tests.sh,
# ci-web-runtime-test.sh and the build job's integration steps), each in its
# own checkout, and kept the runs as test-runs-* artifacts; test-upload.yml
# downloads them all into .forge/tests/runs/. It lives outside ci.yml, which
# is held to exposing no credential (internal/ciguard).
# The Tests pages of the hosted "forge" project then show main's results,
# merged coverage and flaky tests. forge has no deployed env, so the runs go
# to Reliant cloud, forge's default control plane.
#
# IT NEVER CHANGES A VERDICT, and it always exits 0: CI gate already judged
# the lanes, and the upload is a copy of what they recorded.
#   - No FORGE_CONTROL_PLANE_TOKEN (a `tests:write` token, minted by the
#     owner and kept as a secret of the `tests-upload` environment, which
#     only test-upload.yml, on main's pushes, reaches; forge is
#     public, and a fork's pull request never gets it): a warning, the report
#     printed, nothing sent.
#   - A failed upload, or a control plane that does not record test runs yet:
#     forge's own one-line warning.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 0

sha=${1:-}
if [[ -z "$sha" ]]; then
  echo "usage: $0 <sha>" >&2
  exit 0
fi
warn() {
  if [[ -n "${GITHUB_ACTIONS:-}" ]]; then echo "::warning title=Test results not uploaded::$*"; else echo "ci-test-upload: $*" >&2; fi
}

if ! ls .forge/tests/runs/*/run.json >/dev/null 2>&1; then
  warn "no recorded test run under .forge/tests/runs/ for ${sha:0:12}; nothing to report"
  exit 0
fi

upload=--upload
if [[ -z "${FORGE_CONTROL_PLANE_TOKEN:-}" ]]; then
  warn "FORGE_CONTROL_PLANE_TOKEN is not set (the tests-upload environment's secret, an owner step): reporting locally only"
  upload=--upload=false
fi

scripts/ci-forge.sh test report --commit "$sha" "$upload" || echo "ci-test-upload: forge test report exited $? (no verdict changes)"
exit 0
