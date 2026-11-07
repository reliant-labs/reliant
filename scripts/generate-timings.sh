#!/usr/bin/env bash
# generate-timings.sh <notes-file> <command>...
#
# Runs <command> (the merge gate's `task generate:forge`) with
# FORGE_GENERATE_TIMINGS=1, passes its output through unchanged, and appends a
# "generate slowest steps: ..." line to <notes-file> naming the five slowest
# steps of forge's timing table. forge prints that table on stderr, so stderr
# is captured with stdout. Exits with <command>'s own status.
set -uo pipefail
notes=${1:?usage: generate-timings.sh <notes-file> <command>...}
shift
raw=$(mktemp "${TMPDIR:-/tmp}/generate-timings.XXXXXX") || exit 1
trap 'rm -f "$raw"' EXIT
FORGE_GENERATE_TIMINGS=1 "$@" 2>&1 | tee "$raw"
rc=${PIPESTATUS[0]}
steps=$(sed -n '/generate step timings/,$p' "$raw" | sed -n '2,6p' | sed -E 's/^ +//; s/ +/ /g' | paste -sd ';' - | sed 's/;/; /g')
[ -z "$steps" ] || echo "generate slowest steps: $steps" >>"$notes"
exit "$rc"
