# proto-vendor/controlplane — GENERATED, do not edit

The public control-plane API, exported verbatim from the control-plane repo's
`proto/` by `.github/scripts/sync-controlplane-proto.mjs`. Which services are
public is decided by control-plane's `proto/public-api.txt`; no operator-only
(`*Admin`) or internal-service API is included.

Nothing here is authored in this repo. To change the contract, change the
proto in control-plane, merge it, then run `task contract:controlplane:sync`
(the sync script, then the client regeneration) against control-plane main.

`SOURCE` names the control-plane commit this tree was exported from: this
repo's pin of the contract. CI (`task contract:controlplane:check`) fails if
the tree is not exactly what that commit exports, or if that commit is not on
control-plane main. Being behind control-plane main is not a failure here:
`.github/workflows/cp-contract-drift.yml` keeps one issue open while it lasts,
and control-plane's own gate blocks a change this copy could not survive.

Generated clients:

- TypeScript: `frontends/web/src/gen/controlplane/` (e.g. `services/deploy/v1/deploy_pb.ts`)
- Go:         `gen/controlplane/` (e.g. `services/billing/v1`)

## Status: EXPERIMENTAL

controlplane.v1 is unstable: field numbers, RPC names and service shapes may
change without notice until the package is declared stable.
