#!/usr/bin/env bash
# The gate `forge vuln patch --verify` runs in this repository (forge.yaml's
# vuln.patch.verify), after it bumped vulnerable dependencies in place:
#
#   scripts/vuln-patch-verify.sh
#
# It verifies what the bump changed, against HEAD:
#   go.mod / go.sum              go build ./..., then the -short tier
#                                (task test:short: every package, cached)
#   web-runtime/package*.json    npm ci, typecheck and the Vitest suite in
#                                web-runtime/ (npm test builds dist/ first)
# A tree where neither changed (a named advisory that needed nothing, or a run
# by hand) verifies both. The patcher's default, `task test:short` alone,
# would pass a web-runtime bump without running a single web-runtime test.
#
# Exit status: 0 everything it ran passed, else the first failure's.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

changed() { ! git diff --quiet HEAD -- "$@"; }
go_changed=false web_changed=false
changed go.mod go.sum && go_changed=true
changed web-runtime/package.json web-runtime/package-lock.json && web_changed=true
if [[ $go_changed == false && $web_changed == false ]]; then
  go_changed=true web_changed=true
fi

if [[ $go_changed == true ]]; then
  echo "→ go build ./... && task test:short"
  go build ./...
  task test:short
fi
if [[ $web_changed == true ]]; then
  echo "→ web-runtime: npm ci, typecheck, test"
  (cd web-runtime && npm ci --no-audit --no-fund && npm run typecheck && npm test)
fi
