#!/usr/bin/env bash
# The test process must not inherit a per-invocation host-slot lease token.
# internal/cli's TestMain clears FORGE_SLOT_HELD already, but Go's test cache
# records getenv calls against the ORIGINAL parent environment, even after a
# test unsets a variable. Every `forge slot run` (including a Reliant heavy
# command) supplies a fresh token, so otherwise every gate recompiles nothing
# but reruns the entire cli suite on an unchanged tree.
#
# The same holds for the run that records these tests. `forge test capture`
# and `forge test flakes gate` export FORGE_TEST_RUN, FORGE_TEST_LANE and
# FORGE_TEST_LANE_DIR to the command they record, and FORGE_TEST_RUN names a
# new run directory every time. forge's own tests read FORGE_TEST_RUN (the
# begin/capture tests, through os.Getenv), so it would key their cached
# results on a fresh value and internal/cli would never replay; it would also
# let a test of `forge test begin` join the CI run that is recording it. The
# lane's coverage path is passed as -coverprofile, which Go's cache ignores.
#
# Remove them before cmd/go starts. The gate's build/generate processes retain
# their lease; test fixtures provision their own slots and cli's TestMain
# disables ambient heavy slots. All other environment and test flags survive.
# Shared by the PR shards and changed-test repetition, including main's warmer.
set -euo pipefail
unset FORGE_SLOT_HELD FORGE_TEST_RUN FORGE_TEST_LANE FORGE_TEST_LANE_DIR
exec go test "$@"
