#!/usr/bin/env bash
# E2E Scaffold on this machine: `forge project new` from the forge built at
# this checkout, then the generated project's own gates, as
# .github/workflows/e2e-scaffold.yml runs them on a runner. scripts/gate.sh
# runs it as its `scaffold` lane (the Reliant merge queue's gate).
#
#   scripts/ci-e2e-scaffold.sh      exit 0 pass, 1 a step failed, 2 a tool is missing
#
# The steps are the workflow's, in its order, and its header says why each
# exists: install forge + contractlint, forge tools install, serve this
# checkout's web-runtime to npm (internal/npmoverlay), project new
# (--link-forge), replace forge with this checkout in go.mod and gen/go.mod,
# forge generate, wait for the module proxy to serve the pinned forge, go mod
# tidy, go build, forge lint, go test, then the frontend's frozen pnpm
# install, typecheck, build and vitest. A change to one is a change to both.
#
# Everything lives in a scratch directory of its own (SCAFFOLD_DIR, default a
# fresh temp dir): the binaries, the overlay and the project. Nothing is
# installed into the machine's GOBIN or PATH. SCAFFOLD_KEEP=1 keeps the
# directory for debugging; otherwise it is removed on exit.
set -euo pipefail

root=$(git rev-parse --show-toplevel)
cd "$root"

for tool in go node pnpm buf; do
  command -v "$tool" >/dev/null 2>&1 || { echo "required tool(s) not on PATH: $tool (E2E Scaffold needs go, node, pnpm and buf)"; exit 2; }
done

work=${SCAFFOLD_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/forge-e2e-scaffold.XXXXXX")}
mkdir -p "$work"
overlay_pid=""
cleanup() {
  if [[ -n "$overlay_pid" ]]; then kill "$overlay_pid" 2>/dev/null || true; wait "$overlay_pid" 2>/dev/null || true; fi
  if [[ "${SCAFFOLD_KEEP:-0}" != 1 ]]; then rm -rf "$work"; else echo "kept $work"; fi
}
trap cleanup EXIT

step() { echo "── $*"; }

step "install forge + contractlint into $work/bin"
export GOBIN="$work/bin" PATH="$work/bin:$PATH"
go install ./cmd/forge ./cmd/contractlint
forge --version || true

step "forge tools install"
forge tools install

step "serve this checkout's web-runtime to npm"
go build -o "$work/npm-overlay" ./internal/npmoverlay/cmd/npm-overlay
env_file="$work/npm-overlay.env"
"$work/npm-overlay" -pack web-runtime -env-file "$env_file" >"$work/npm-overlay.log" 2>&1 &
overlay_pid=$!
for _ in $(seq 1 600); do
  [[ -s "$env_file" ]] && break
  if ! kill -0 "$overlay_pid" 2>/dev/null; then
    cat "$work/npm-overlay.log"
    echo "npm overlay exited before it was ready"
    exit 1
  fi
  sleep 1
done
[[ -s "$env_file" ]] || { cat "$work/npm-overlay.log"; echo "npm overlay not ready after 10m"; exit 1; }
while IFS= read -r kv; do [[ -n "$kv" ]] && export "${kv?}"; done <"$env_file"
cat "$work/npm-overlay.log"

step "forge project new e2e-test"
mkdir -p "$work/e2e"
# --forge-root names this checkout: a forge built with -trimpath (GOFLAGS)
# carries no path to find it from.
(cd "$work/e2e" && forge project new e2e-test --kind service --mod github.com/e2e/e2e-test --service api --frontend admin --link-forge --forge-root "$root")
proj="$work/e2e/e2e-test"
cd "$proj"

step "bridge the scaffold to this checkout's forge module"
for mod in . gen; do
  (cd "$mod" && go mod edit -replace "github.com/reliant-labs/forge=$root")
done

step "forge generate"
forge generate

step "wait for the module proxy to serve the pinned forge version"
pinned=$(awk '$1 == "github.com/reliant-labs/forge" { print $2; exit }' go.mod)
[[ -n "$pinned" ]] || { sed -n '1,40p' go.mod; echo "the scaffolded go.mod does not require github.com/reliant-labs/forge"; exit 1; }
"$root/scripts/wait-for-go-proxy.sh" --module github.com/reliant-labs/forge --version "$pinned" --tag-pushed

step "go mod tidy"
go mod tidy
step "go build"
go build ./...
step "forge lint"
forge lint
step "go test"
go test ./...

step "frontend: pnpm install, typecheck, build, test"
fe=$(find frontends -mindepth 1 -maxdepth 1 -type d 2>/dev/null | head -n1 || true)
[[ -n "$fe" ]] || { echo "no frontend under frontends/: the scaffold was invoked with --frontend admin"; exit 1; }
cd "$fe"
pnpm install --frozen-lockfile --prefer-offline
pnpm run typecheck
pnpm run build
pnpm test --silent
echo "E2E Scaffold: passed"
