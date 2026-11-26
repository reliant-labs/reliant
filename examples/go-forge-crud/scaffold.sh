#!/usr/bin/env bash
# Scaffold the go-forge-crud example: a Go Connect-RPC API with one entity
# (Task) and its generated CRUD, sized for an anonymous (--claimable) deploy.
#
#   ./scaffold.sh [dir]      # default dir: ./tasks-api
#
# Needs: go, and `reliant forge` (or `npx -y @reliantlabs/forge@latest`,
# set FORGE="npx -y @reliantlabs/forge@latest").
set -euo pipefail

dir="${1:-tasks-api}"
forge="${FORGE:-reliant forge}"

$forge project new "$dir" --mod example.com/tasks-api --service tasks --claimable --harness none
cd "$dir"

# The one hand-written input: mark a message as an entity. `forge scaffold`
# births its migration, the CRUD quintet, the ORM and the handler wiring.
cat >>proto/services/tasks/v1/tasks.proto <<'PROTO'

// forge:entity
message Task {
  string title = 2;
  bool done = 3;
}
PROTO

$forge scaffold
go build ./...
echo "scaffolded $dir: next, cd $dir && $forge env deploy prod --claimable --plan-only"
