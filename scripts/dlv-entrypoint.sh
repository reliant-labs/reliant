#!/bin/sh
# dlv-entrypoint.sh — wrap `reliant <args>` in a headless Delve server.
#
# Used only by Dockerfile.debug (the dlv-enabled image). Lets a debugger
# attach over the network to an in-cluster reliant role (daemon-gateway, api,
# worker) without changing any reliant source.
#
#   The container CMD (e.g. `server gateway`) is passed through to reliant:
#     dlv exec --headless ... /usr/local/bin/reliant -- server gateway
#
# Env knobs:
#   DELVE_LISTEN   listen address for the Delve server (default :2345)
#   DELVE_CONTINUE if "1"/"true", start the target immediately instead of
#                  halting at entry (so the pod becomes Ready without a
#                  debugger attached first). Default: halt at entry.
set -eu

DELVE_LISTEN="${DELVE_LISTEN:-:2345}"

# Capture the container CMD (reliant's args) before we rebuild $@ with dlv
# flags.
reliant_args="$*"

# Base dlv flags. --accept-multiclient lets a debugger attach/detach
# repeatedly while the process keeps running (forge debug stop detaches; the
# pod stays up).
continue_flag=""
case "${DELVE_CONTINUE:-}" in
    1 | true | TRUE) continue_flag="--continue" ;;
esac

# `--` separates dlv flags from the target binary + its args. word-splitting
# reliant_args is intentional (server gateway -> two args).
# shellcheck disable=SC2086
exec dlv exec \
    --headless \
    --listen="${DELVE_LISTEN}" \
    --api-version=2 \
    --accept-multiclient \
    ${continue_flag} \
    /usr/local/bin/reliant -- ${reliant_args}
