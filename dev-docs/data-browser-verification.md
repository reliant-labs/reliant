# Data browser: authorization evidence, live verification and recovery

This is the verification contract for Reliant #2339. Client fixtures prove client
behavior, not server authorization. Vendoring an RPC does not install its server,
create a database grant or enable production direct reach. No production action
was taken to write this document. Private backend source is not copied here.

## Immutable backend and accepted execution evidence

[SOURCE](../proto-vendor/controlplane/SOURCE) names control-plane
**8901240e0ff6fbdbc099d0284ff72ca07ee62e9c**, the actual main merge of
[control-plane #2128](https://github.com/reliant-labs/control-plane/pull/2128).
Its prerequisite [#2175](https://github.com/reliant-labs/control-plane/pull/2175)
merged as f87e731ba1b9b3b8629141f1bc25579772205656. The contract export was
reproduced and required to be on main with `task contract:controlplane:check`.

The backend's [carried exact-head review](https://github.com/reliant-labs/control-plane/pull/2128#pullrequestreview-5818860886)
records review of 74fd686edb85b8a16e4448eaa92bc5a725b5c452 carried through the
pipeline's generated-only merge to 96cd99958545d50789a7b1ad86ae37c2bc6cf0d0.
That head then passed cp-merge and landed at SOURCE. Earlier merge-gate failure
was only project-local goimports grouping; its verify-generated, unit and frontend
lanes passed. The formatter was corrected before the successful merge.

Executed author checks, not reviewer execution claims: backend scoped build,
app/appdb/handler/dbpanel short suites and conventions/contract passed; adapter
TLS short/race tests passed; earlier full handler integration ran 172 tests with
zero skipped, adapter integration 16 with zero skipped. Current UI frozen install,
Data/demo tests, types and touched lint passed; the canonical row-state fix has
its own grid → row → Back regression. Historical failed host/setup/formatter
runs are not described as passes. Normal pipeline gates remain responsible for
the exact proposed UI head.

Credentialed read-only reproduction (no production database/registry required):

```bash
# control-plane checkout
git cat-file -e 8901240e0ff6fbdbc099d0284ff72ca07ee62e9c^{commit}
git merge-base --is-ancestor 8901240e0ff6fbdbc099d0284ff72ca07ee62e9c origin/main
# reliant checkout; the directory holds the referenced backend commit
CONTROL_PLANE_DIR=../control-plane task contract:controlplane:check
```

An inaccessible private permalink is not proof a test passed. Authorized
reviewers can inspect the immutable links below; otherwise retain that reach
limitation or obtain a maintainer's authorized snapshot. Do not replace server
proof with client mocks, publish private source, or infer missing tests from an
anonymous 404. This follows the same evidence separation as the accepted
[Config verification contract](https://github.com/reliant-labs/reliant/blob/d9a699414145bcd0d833048b1452b820c751f531/dev-docs/workload-config-verification.md).

## Server proof map at SOURCE

| Boundary / negative case | Immutable implementation and regression evidence | Scope |
|---|---|---|
| Live membership, db:query scope/grant, policy and confirmation | [browser admission](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/handlers/app_database/data_browser.go#L32-L89), [authorization/policy](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/handlers/app_database/rpc_query_database.go#L117-L224), [Browse denial table](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/handlers/app_database/handlers_scaffold_browse_table_test.go#L113-L195) | Stranger NotFound, missing grant PermissionDenied before policy, deny/unknown policy refuses before database lookup, audited org token refused, missing/wrong-env confirmation and blank reason refused. UI confirmation is not this authority. |
| List/Describe enforce the same admission | [List tests](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/handlers/app_database/handlers_scaffold_list_browsable_databases_test.go), [Describe tests](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/handlers/app_database/handlers_scaffold_describe_database_test.go), [real handler/store/audit integration](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/handlers/app_database/data_browser_integration_test.go#L59-L208) | Untagged denial tests are eligible in short/affected tests; container-backed integration is the separate integration lane. A merged status alone does not prove every lane ran. |
| Tenant shape cannot select platform credentials | [exact operator binding](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/appdb/direct_routes.go), [negative matrix](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/appdb/direct_routes_test.go#L42-L98) | Other org/env, changed host/namespace/cluster/port/db/role/secret/key/TLS/reach/alias deny. Cached descriptions cannot bypass; zero adapter calls means no password-file read or dial; denial audits exclude credential selectors. |
| Required encryption and reader admission | [connection/role check](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/appdb/external.go#L19-L73), [TLS success/refusal tests](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/appdb/appdbexternal/tls_test.go#L32-L202), [rotation/writable-role integration](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/appdb/appdbexternal/dial_integration_test.go) | Empty/default and require negotiate TLS >=1.2, no plaintext fallback; writable role refused per connection, rotated password reread. require intentionally does not authenticate the pooler certificate; separate enablement review must accept that existing contract. |
| Bounded reads, audit withholding and readable catalog | [Describe/Browse execution](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/appdb/browse.go#L27-L180), [audit regressions](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/appdb/browse_test.go) | One logical read audit per Describe/Browse, withheld rows on successful-call audit failure; page/count statements bounded, role-readable catalog only. Client fixture totals are not billing quantities. |
| Production enablement is separate | [disabled render proof](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/ingress/prod_data_browser_reach_render_test.go), [backend rollout contract](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/docs/data-browser-rollout.md) | No direct API credential mount or operator binding is enabled here. No authoritative IDs are invented; internal-console fallback remains. |

Other newly exported GetWorkloadConfig/MintLocalTelemetryToken/GetUser contracts
arrive because the vendor sync exports the whole public surface, not because this
Data UI calls those RPCs or reimplements their authorization. Their existing
server behavior is outside this UI implementation; additive generated clients
are not evidence of server enforcement. The Data API adapter calls only the
listed database RPCs and treats unsupported old servers explicitly.

## Client proof and limits

The real browser harness tests audited confirmation before reads, denied policy
with zero reads, permission/unavailable/unsupported states, pagination/count caps,
truncation, foreign-key links and cancellation. The row-open fix preserves only
validated DataSearch (database/table/filter/sort/columns/offset/size plus row),
not access reasons or credentials in URLs. The grid → row → Back regression
starts on the actual filtered/sorted/paginated grid, not a synthetic row URL.

Fixtures do not prove fresh authorization after membership/grant changes or
server audit persistence. Query caches are client performance state, not an
access grant; actual authorization must run on fresh server requests. Do not
claim cached data can be retroactively erased from a user's memory/downloads.

## Approved live acceptance — not executed

Use only an owner-approved synthetic org/environment/database and non-secret
rows. Never probe customer identifiers or alter customer grants for this test.
Record release, sanitized request/audit IDs, actor/env/db/outcome and timestamps;
no token/password/row payloads in issues or screenshots.

1. Verify deployed backend supports the SOURCE contract; confirm the actual
   release/frontend identity. Open Data on an unsupported approved test server:
   show unsupported state, not an empty successful table or retry loop.
2. As a test user lacking db:query, List/Describe/Browse must deny. With grant but
   deny/unknown policy, no read succeeds. Foreign org/environment selectors must
   refuse without data or credential access. Confirm UI messages do not echo
   server internals, route selectors or credentials.
3. With approved grant under audited policy, no Describe/Browse before explicit
   matching-env confirmation and nonblank reason. Cancel makes no read. Confirm
   the next authorized request carries the selected env/db, not stale selection.
4. Browse a small approved table; test filter, sort, selected columns, Next and
   grid → row → Back. Verify bounded counts/has-more/next-offset and truncated
   cells are represented, not fabricated full values. Follow an approved FK and
   related-row link; no arbitrary SQL mutation control is exposed.
5. Correlate each authorized Describe/Browse with its audit ID and exactly one
   logical operation audit. Row-related requests can be separate logical reads;
   multiple page/count SQL statements do not justify duplicate logical audit
   rows. Verify actor/env/db/reason/outcome; record IDs only. No audit evidence
   means unknown, not success.
6. Revoke a dedicated test grant/membership or expire its credential using the
   owner's approved mechanism. A **fresh** request must deny. Switch org/env/db
   while a request is pending; no late result appears as the new scope. Restore
   only approved test grants. Existing displayed/downloaded bytes are not undone.
7. For direct routes, retain production unavailable until authoritative exact
   operator bindings and mounts are separately reviewed and the backend rollout
   checks pass. A copied platform route in another synthetic org/env must deny
   before secret access/dial, including after a prior cached description.
8. Only in an isolated approved test deployment inject audit failure; no rows
   may return. Never tamper with shared production audit infrastructure. If no
   injection/canary access is available, mark not executed and escalate.

## Recovery

On client interoperability failure, stop adoption and ship a reviewed fix-forward
that disables/removes only Data navigation/read actions while preserving other
console pages and internal-console fallback. No undocumented feature flag is
promised. Owner-approved endpoint containment may refuse new database reads;
never widen permissions or weaken route checks, reader admission, TLS or audit
withholding to make UI green. No schema down-migration is needed.

On cross-tenant or sensitive-data exposure, stop reads/testing, notify incident
owners and apply approved server-side containment; hiding UI alone is not a
security boundary. Assess delivered bytes/caches/exports and rotate affected
credentials under incident policy, preserving sanitized IDs and audit evidence.
The author does not deploy/rollback production. Release owners alone choose any
coherent recovery artifact under policy; then fix forward and repeat approved
live acceptance before re-enabling Data. Live checks remain pending here.
