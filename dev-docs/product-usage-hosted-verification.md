# Product-usage dashboard: hosted verification and containment

The canonical input is Forge's product-usage generator, projected into
`deploy/observability/spec/project_gen.json`. Its actual dashboard id is `usage`
(the UI can display “Product usage”), with nine SQL panels. Do not hand-edit the
projection or its hash to hide a panel. The #2468 input at public Reliant commit
`39f4583cb6d90ddeaaad424fbd508d35c0eeaffe` has SHA-256
`0bac08970965d4e235f0bd3265574ee37d37ce0f08ab83f0655acec733fd2d8e`.

## Boundaries and executable evidence

The public consumer path is the actual generated JSON →
`services/controlPlane/observability/dashboardsApi.ts` `panelFromJson` →
`api.ts` `queryPanel` → typed hosted `ObservabilityService.QueryPanel`. The
browser sends panel SQL and org/project/environment/service scope; it does not
choose a database or supply ClickHouse credentials. The public test
`productUsageDeployment.test.ts` reads this actual deployment JSON, normalizes
all nine panels with the production parser, exercises the production adapter,
checks scope/result mapping and authorized restricted-empty response mapping. That
mocked transport contract is **not** an authorization or database-isolation proof.

The private hosted execution path is:

- control-plane `internal/handlers/observability/rpc_query_panel.go` and
  `rpc_query_panel_sql.go`: resolve and validate panel, open admitted scope,
  execute SQL through the tenant reader and fold actual results;
- `internal/obsread/service.go`: live membership/environment admission before
  acquiring the tenant connection; derive the database from the admitted org;
- `internal/obsread/sql.go`: bind the verified scope, require connection ownership,
  choose that org's database in execution settings, and run under its read-only
  ClickHouse user. A SQL string is not the tenant boundary: the selected database
  **and database grants** are. An authorized project with no live environments
  binds an unmatchable environment sentinel and returns zero rows; it must never
  mean unrestricted. Foreign org/environment requests are PermissionDenied
  before any tenant connection or query.

Private source stays private. Named hosted integration evidence is
`TestConsumerProductUsageHostedTenantIsolation`, in
`internal/handlers/observability/integration_product_usage_test.go`. It reads the
immutable public consumer commit above and fails on a hash mismatch, then uses
the typed HTTP client, real hosted handler/read gate, real Postgres membership,
and real provisioned ClickHouse tenant databases/users. It executes all nine
panels and verifies disjoint A/B events, event counts and distinct actors,
environment/service filters, restricted-empty zero rows, cross-org/environment
and revoked-member refusal before `ConnFor`/`Query`, and actual database-grant
refusal of explicitly qualified foreign SQL. This test belongs to the private
integration lane and must actually execute before shipment; a short-lane skip
is not a pass. Private test PR control-plane #2315, commit `a22d5568e`, final actual execution
**PASS 23.39s**. Its code is not reproduced here. The surrounding package's short
tests and GOWORK=off build also pass. The full integration task wrapper could not
start because the machine PATH Forge differed from that private project's older
pin; that wrapper is not claimed passed. The new named integration test did
actually execute, without -short, against real infrastructure.

The exact pinned Forge `0a3a89d52a00` test
`internal/obsdash/TestProductUsageSQLOnClickHouse` was actually executed against
ClickHouse 26.8.29.10 (the platform's pinned image digest), **PASS 21.23s**, with
`FORGE_ALERT_CLICKHOUSE` set to a dedicated test container and without `-short`.
It covers every generated panel and fixed-time active/cohort/retention meaning;
it is complementary to, not a substitute for, hosted consumer isolation.

## Post-deploy acceptance (not executed by this PR)

Only the normal reviewed release train deploys this change. Record the promoted
release digest, Reliant/Forge/control-plane commits and timestamp. Then:

1. Run `reliant forge obs check prod --wait --json` from the owning project.
   Exit 0 is healthy; 1 requires investigation/fix-forward; 2 is **not proven
   good**; 8 needs more data and a later check. Follow the returned suggested
   issue commands. Do not infer panel authorization from a green health check.
2. Use two explicitly approved **synthetic** test organizations A/B, their own
   console sessions, and separately owned live environments A-prod/A-stage/B-prod.
   Do not insert artificial events into real customer tenants. Record principal,
   org, environment and service IDs without tokens or personal event payloads.
3. Emit known approved synthetic product events through normal ingest: distinct
   actor IDs and event-name sentinels in A-prod/api, A-prod/worker, A-stage/api,
   and B-prod/api. Record the bounded time range and expected counts.
4. Through the actual hosted console/API run all nine `usage` panels. Confirm
   events-by-name counts and active-user cardinalities for A and B separately;
   narrow A to prod, then worker, and confirm only those sentinels/counts. Check
   retention/new-vs-returning with the recorded window; an empty cohort is a
   legitimate empty result, not an infrastructure failure.
5. Request an authorized project with zero live environments: expect no event
   rows, never all-org results. Change A's request org to B, or name B's environment
   under A: expect PermissionDenied without query execution. Revoke the approved
   synthetic membership and repeat: PermissionDenied. Do not weaken auth or use
   an admin session to make the checks pass.
6. Inspect hosted query logs/audit by panel/request correlation and tenant database
   (no payload/token dump). Confirm the admitted org's database/user, no foreign
   query on denied requests, no unknown columns, parse failures, permission noise
   for authorized panels, or quota/latency regression. Archive the bounded
   sanitized evidence with the release verification record.

## Containment and recovery

If foreign rows/cardinalities appear, stop acceptance and halt release promotion.
Treat it as a tenant-isolation incident. Temporarily deny the affected synthetic
or application principals' raw-query/observability access at the supported grant
boundary (through the owning organization), or withhold the affected dashboard
from serving through an approved owned dashboard declaration. A UI-only hide is
not containment because the RPC still exists. Keep unrelated tenants' grants
unchanged. Do not hand-edit generated output, broaden query grants, bypass
membership, disable the tenant gate, or claim a rollback of an incompatible
schema. Preserve bounded sanitized request/database evidence, fix the canonical
input or owning executor, run both real isolation suites again, and ship a
compatible **fix-forward** through normal review/release. Re-enable only after
approved synthetic acceptance passes. No live verification/deployment is claimed
by the tests or this document.
