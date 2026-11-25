#!/usr/bin/env bash
# Run web-runtime's Vitest suite as a recorded test lane, with coverage.
#
#   scripts/ci-web-runtime-test.sh
#
# ci.yml's checks job runs it after installing web-runtime's npm
# dependencies (`npm install` runs the package's prepare script, which builds
# dist/, the tests that read the built bundle read). Locally:
# `(cd web-runtime && npm ci) && scripts/ci-web-runtime-test.sh`.
#
# WHY HERE. Until this lane, web-runtime's ~1,000 tests ran only when the
# package was released (scripts/release-web-runtime.sh, release-npm.yml), so a
# change that broke one merged and was found at the next publish. The suite
# takes ~15s, inside the merge tier's budget.
#
# RECORD. forge dogfoods its own test record for its frontend lane as it does
# for Go (scripts/ci-test-shard.sh): `forge test capture --lane frontend`
# (this checkout's forge, scripts/ci-forge.sh) runs Vitest unchanged and
# copies its JSON report into .forge/tests/runs/<run>/frontend/ as the
# lane's results; @vitest/coverage-v8 writes the lane's Istanbul coverage
# (coverage-final.json) of src/ (not the built dist/ some tests load) straight
# into the lane directory. clean=false: the
# lane directory is capture's, and Vitest's default would empty it first.
# The exit status is Vitest's. FORGE_TESTS=off records nothing.
set -euo pipefail
root=$(git rev-parse --show-toplevel)
run=$("$root/scripts/ci-forge.sh" test begin)
lane="$run/frontend"
mkdir -p "$lane"
# The JSON report lives outside the lane: capture copies it in after Vitest
# exits.
report=$(mktemp -d)/vitest.json
cd "$root/web-runtime"
exec "$root/scripts/ci-forge.sh" test capture --run "$run" --lane frontend --results "$report" -- \
  npx vitest run --reporter=default --reporter=json --outputFile.json="$report" \
  --coverage.enabled --coverage.provider=v8 --coverage.clean=false --coverage.include='src/**' \
  --coverage.reporter=json --coverage.reporter=text-summary --coverage.reportsDirectory="$lane"
