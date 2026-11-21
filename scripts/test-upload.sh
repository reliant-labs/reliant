#!/usr/bin/env bash
# test-upload.sh — send a commit's recorded test runs to the control plane:
# `forge test report --commit <sha> --upload`, best effort.
#
#   scripts/test-upload.sh <sha>
#
# ci.yml's test-upload job runs it on main, after the release gate's parts:
# their lanes (unit, frontend, integration, e2e; scripts/test-capture.sh)
# were recorded in each part's private checkout and handed over as artifacts
# into .forge/tests/runs/. The Tests pages of the reliant project then show
# main's results, coverage and flaky tests (Tests D3b).
#
# IT NEVER CHANGES A VERDICT, and it always exits 0: the release record is
# the release gate's, and the upload is a copy of what it already judged.
#   - No FORGE_CONTROL_PLANE_TOKEN (a `tests:write` token, minted by the owner
#     and kept as a secret of the `tests-upload` environment, which only
#     main's pushes reach; reliant is public, and a fork's pull request never
#     gets it): a warning, nothing sent.
#   - A pinned forge without `forge test report --upload` (before v1.10.0):
#     a warning, nothing sent.
#   - A failed upload, or a control plane that does not record test runs yet:
#     forge's own warning.
# The report itself (text: results, merged coverage, diff coverage) is printed
# either way, so the job's log shows what main's lanes measured.
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

upload=--upload
if ! scripts/forge.sh test report --help 2>/dev/null | grep -q -- '--upload'; then
  warn "the pinned forge has no \`forge test report --upload\` (it ships in forge v1.10.0): reporting locally only"
  upload=""
elif [ -z "${FORGE_CONTROL_PLANE_TOKEN:-}" ]; then
  warn "FORGE_CONTROL_PLANE_TOKEN is not set (the tests-upload environment's secret, an owner step): reporting locally only"
  upload=--upload=false
fi

# shellcheck disable=SC2086 # upload is one flag or none
scripts/forge.sh test report --commit "$sha" $upload || echo "test-upload: forge test report exited $? (no verdict changes)"
exit 0
