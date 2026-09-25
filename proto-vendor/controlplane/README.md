# proto-vendor/controlplane — GENERATED, do not edit

The public control-plane API, exported verbatim from the control-plane repo's
`proto/` by `.github/scripts/sync-controlplane-proto.mjs`. Which services are
public is decided by control-plane's `proto/public-api.txt`; no operator-only
(`*Admin`) or internal-service API is included.

Nothing here is authored in this repo. To change the contract, change the
proto in control-plane, then re-run the sync script and
`npm run proto:generate:controlplane`. control-plane's CI fails if this tree
differs from what its protos export.

Generated clients:

- TypeScript: `web/src/gen/controlplane/` (e.g. `services/deploy/v1/deploy_pb.ts`)
- Go:         `gen/controlplane/` (e.g. `services/billing/v1`)

## Status: EXPERIMENTAL

controlplane.v1 is unstable: field numbers, RPC names and service shapes may
change without notice until the package is declared stable.
