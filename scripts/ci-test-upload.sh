#!/usr/bin/env bash
# Send a commit's recorded test runs to the control plane:
# `forge test report --commit <sha> --upload`, best effort.
#
#   scripts/ci-test-upload.sh <sha>
#
# test-upload.yml runs it when a CI run of a push to main, or of a merge-queue
# entry, completes. That run's jobs recorded their lanes through
# `forge test capture` (scripts/ci-test-shard.sh, ci-test-full-shard.sh,
# ci-changed-tests.sh, ci-web-runtime-test.sh and the build job's integration
# steps), each in its own checkout, and kept the runs as test-runs-*
# artifacts; test-upload.yml downloads them all into .forge/tests/runs/. It
# lives outside ci.yml, which is held to exposing no credential
# (internal/ciguard).
# The Tests pages of the hosted "forge" project then show main's and the merge
# queue's results, merged coverage and flaky tests. forge has no deployed env,
# so the runs go to Reliant cloud, forge's default control plane.
#
# THE CREDENTIAL IS FORGE'S TO FIND. The script always asks for the upload, and
# forge resolves it in its own order: FORGE_CONTROL_PLANE_TOKEN (a `tests:write`
# token, a secret of the `tests-upload` environment, which only the push-to-main
# job reaches), else the job's GitHub OIDC identity (`permissions: id-token:
# write`), exchanged at the control plane. When neither works, forge says why,
# as a `Test results not uploaded` warning on the run's summary.
#
# A merge-queue entry's commit is on no branch of this checkout (the job runs
# main's own code, and the entry is data): it is fetched by sha first.
#
# IT NEVER CHANGES A VERDICT, and it always exits 0: CI gate already judged
# the lanes, and the upload is a copy of what they recorded.
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

if ! git cat-file -e "${sha}^{commit}" 2>/dev/null; then
  if ! git fetch --quiet --no-tags origin "$sha"; then
    warn "commit ${sha:0:12} is not in this clone and could not be fetched; nothing reported"
    exit 0
  fi
fi

scripts/ci-forge.sh test report --commit "$sha" --upload || echo "ci-test-upload: forge test report exited $? (no verdict changes)"
exit 0
