#!/usr/bin/env bash
# Fast hermetic generator-boundary test; no Go compile, network or real codegen.
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
mkdir -p "$scratch/repo/scripts" "$scratch/repo/gen/example"
cp "$root/scripts/proto-generate-atomic.sh" "$scratch/repo/scripts/"
printf 'old output\n' > "$scratch/repo/gen/example/output"
cat > "$scratch/fake-buf" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" > "$ARGS"
[[ $1 == --timeout && $2 == "${EXPECTED_TIMEOUT}" && $3 == generate ]]
while [[ $# -gt 0 ]]; do
  if [[ $1 == -o ]]; then output=$2; break; fi
  shift
done
mkdir -p "$output/gen/example"
printf 'new output\n' > "$output/gen/example/output"
[[ ${FAIL_GENERATE:-0} == 0 ]]
FAKE
chmod +x "$scratch/fake-buf"
export BUF="$scratch/fake-buf" ARGS="$scratch/args" EXPECTED_TIMEOUT=10m
bash "$scratch/repo/scripts/proto-generate-atomic.sh" fixture.yaml gen/example
[[ $(cat "$scratch/repo/gen/example/output") == 'new output' ]]
export BUF_TIMEOUT=27s EXPECTED_TIMEOUT=27s
bash "$scratch/repo/scripts/proto-generate-atomic.sh" fixture.yaml gen/example
[[ $(sed -n '2p' "$ARGS") == 27s ]]
printf 'preserve on timeout\n' > "$scratch/repo/gen/example/output"
if FAIL_GENERATE=1 bash "$scratch/repo/scripts/proto-generate-atomic.sh" fixture.yaml gen/example; then
  echo 'expected generator failure' >&2; exit 1
fi
[[ $(cat "$scratch/repo/gen/example/output") == 'preserve on timeout' ]]
echo 'PASS: bounded configurable timeout and atomic failure preservation'
