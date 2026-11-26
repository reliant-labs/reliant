#!/usr/bin/env bash
# Scaffold the nextjs-static-go-api example: a Go Connect-RPC API with one
# entity (Note), its Postgres table, and a Next.js static-export frontend that
# calls it through generated hooks. No sign-in (--auth-mode none), sized for
# an anonymous (--claimable) deploy.
#
#   ./scaffold.sh [dir]      # default dir: ./notes
#
# Needs: go, node, and `reliant forge` (or set
# FORGE="npx -y @reliantlabs/forge@latest").
set -euo pipefail

dir="${1:-notes}"
forge="${FORGE:-reliant forge}"

$forge project new "$dir" --mod example.com/notes --service notes --frontend web \
  --auth-mode none --claimable --harness none
cd "$dir"

# The hand-written input: one entity.
cat >>proto/services/notes/v1/notes.proto <<'PROTO'

// forge:entity
message Note {
  string title = 2;
  string body = 3;
}
PROTO

$forge scaffold

# --auth-mode none means nobody can sign in, but `forge scaffold` still writes
# the CRUD rpcs with auth_required: true, so each would answer 401. Open them
# (a public notes board), then regenerate.
sed -i.bak 's/auth_required: true/auth_required: false/' proto/services/notes/v1/notes.proto
rm proto/services/notes/v1/notes.proto.bak
$forge generate
go build ./...
(cd frontends/web && npm run build)
echo "scaffolded $dir: next, cd $dir && $forge env deploy prod --claimable --plan-only"
