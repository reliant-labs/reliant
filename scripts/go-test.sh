#!/usr/bin/env bash
# go-test.sh — `go test "$@"`, minus the variables a `forge test` recording
# exports to the command it records.
#
# `forge test capture` and `forge test flakes gate` export FORGE_TEST_RUN,
# FORGE_TEST_LANE and FORGE_TEST_LANE_DIR to the lane's command (a nested
# `forge test begin` and Vitest's coverage directory read them), and
# FORGE_TEST_RUN names a new run directory every time. Go's test cache keys
# a result on every environment variable the test process reads, so a test
# that read one would never replay from the cache again. No reliant test reads
# them today; dropping them here keeps it that way for the next one, and
# for any forge library a test links (forge's own tests do read them:
# forge#2626). -coverprofile is a cacheable flag whose path Go's cache
# ignores, so a recorded lane replays exactly as an unrecorded one.
#
# Every Go lane scripts/test-capture.sh records, and the flake gate
# (scripts/test-flake.sh), run Go through this.
set -euo pipefail
unset FORGE_TEST_RUN FORGE_TEST_LANE FORGE_TEST_LANE_DIR
exec go test "$@"
