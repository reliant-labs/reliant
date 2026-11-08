#!/usr/bin/env bash
# Fast harness: exercise scripts/vet.sh's orchestration without loading any Go
# packages, and pin the existing gate/task entrypoints. No whole-gate run.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/reliant-vet-test.XXXXXXXX")
trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/bin"
cat > "$tmp/bin/go" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$VET_TEST_LOG"
if [ "${VET_TEST_FAIL:-}" = standard ] && [ "$1 $2" = 'vet ./internal/example/...' ]; then exit 19; fi
if [ "${VET_TEST_FAIL:-}" = model ] && [[ "$*" == *-vettool=* ]]; then exit 23; fi
STUB
chmod +x "$tmp/bin/go"
export PATH="$tmp/bin:$PATH" VET_TEST_LOG="$tmp/log"
bash "$root/scripts/vet.sh" ./internal/example/...
[ "$(wc -l < "$tmp/log")" -eq 3 ]
grep -Fx 'vet ./internal/example/...' "$tmp/log"
grep -E '^vet -vettool=.* ./internal/example/\.\.\.$' "$tmp/log"
: > "$tmp/log"
if VET_TEST_FAIL=standard bash "$root/scripts/vet.sh" ./internal/example/...; then exit 1; else [ "$?" -eq 19 ]; fi
[ "$(wc -l < "$tmp/log")" -eq 1 ]
if VET_TEST_FAIL=model bash "$root/scripts/vet.sh" ./internal/example/...; then exit 1; else [ "$?" -eq 23 ]; fi
grep -F 'bash scripts/vet.sh "${vet_pkgs[@]}"' "$root/scripts/merge-gate.sh"
grep -F 'vet) bash scripts/vet.sh ./...' "$root/scripts/release-gate.sh"
grep -F 'bash scripts/vet.sh {{.CLI_ARGS}}' "$root/Taskfile.yml"
echo 'vet wiring: PASS'
