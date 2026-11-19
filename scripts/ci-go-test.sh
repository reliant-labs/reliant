#!/usr/bin/env bash
# The test process must not inherit a per-invocation host-slot lease token.
# internal/cli's TestMain clears FORGE_SLOT_HELD already, but Go's test cache
# records getenv calls against the ORIGINAL parent environment, even after a
# test unsets a variable. Every `forge slot run` (including a Reliant heavy
# command) supplies a fresh token, so otherwise every gate recompiles nothing
# but reruns the entire cli suite on an unchanged tree.
#
# Remove it before cmd/go starts. The gate's build/generate processes retain
# their lease; test fixtures provision their own slots and cli's TestMain
# disables ambient heavy slots. All other environment and test flags survive.
# Shared by the PR shards and changed-test repetition, including main's warmer.
set -euo pipefail
unset FORGE_SLOT_HELD
exec go test "$@"
