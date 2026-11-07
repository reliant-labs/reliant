# Forge b12f3d3e: merged storage/config foundation upgrade

Captured target once from Forge main: `b12f3d3e36e23495d70a784a0e2eea7e99cb5635`
(`v1.1.1-0.20261107180631-b12f3d3e36e2`). Previous pin:
`v1.1.1-0.20261107152223-7a1f5720c58b`, landed by #2387.
This is the next shared upgrade, not a rewrite of CP #2175's older coherent pair.

## Dependency behavior audit

| Merged upstream change | Runtime impact / boundary | Validation |
|---|---|---|
| forge #1899 (`28e6fd22a`): storage scope/shape, blobstore reads | Adds storage:read, recorded buckets, explicit reader signing and opaque read-only key support. No caller receives a grant merely because a scope exists. Strict writes/deletes and Scoped prefix boundary remain. | Existing token exchange intersection tests; new narrow storage/config tokens do not confer reliant:api; AllScopes SQL contract test. Hosted storage remains a separate dependent feature. |
| forge #1901 (`b12f3d3e3`): config-read scope / secret redaction | Adds deploy:config:read; shared secret rule withholds credential headers and malformed/undigested values. This pin exposes no new RPC or config viewer. | Same SQL/admission proofs; config-viewer behavior is independently reviewed in its feature PR. |
| forge #1918/#1922/#1925 (`576994d52`, `ba38f1263`, `9d92addf4`) | Embedded test PG users tracked by leases, retries confirmed bind collisions, fixes isolated test fixture lease. Test/generation lifecycle, not production DB schema/runtime. | Generation and DB proof use supported isolated Postgres16 URL, not a shared-state workaround or a private Go cache. |
| forge #1924 (`99a724b8d`) | Popover render-props scaffold fix; no owned application components replaced. | Generated diff reviewed; no frontend source adaptation needed. |
| forge #1927/#1928 (`775a5ff3a`, `45868126f`) | Dev readiness judges current pods; mismatched k3d certificate produces diagnosis. No production workload declaration changed. | Build/generate/docs; no claim of live cluster rollout proof. |
| forge #1926 (`5927c4b96`) | Adds optional interaction spans to web runtime source. This Go pin does not silently upgrade the published npm runtime. | Existing package/lock pins retained. |

## Additive SQL integration, not a grant

The target's `AllScopes` grows by exactly `deploy:config:read` and `storage:read`.
Focused `forge lint --closed-sets` found the existing `access_tokens_scopes`
CHECK narrower than that catalogue. The allocator-created migrations widen it
with `NOT VALID` under a 5-second lock timeout, then validate separately under
SHARE UPDATE EXCLUSIVE. No existing migration is rewritten, no row is updated,
no org-member grant is inserted, no default scope set is broadened here.

`TestAccessTokensScopesCheckMatchesForge` applies the real migration chain and
requires: validated CHECK, every known scope accepted (both new ones included),
unknown scope rejected, and empty scope array rejected. The admission test adds
both narrow scopes and proves neither is product API authority: accessing ordinary
product RPCs still needs reliant:api. These are control-plane storage/config
capabilities: refusing a narrow token on the Reliant product API does not refuse
it on its separately authorized control-plane RPC. Both product auth helpers keep
the existing fail-closed registry behavior; this PR does not implicitly allow the
new CP scopes on product endpoints. Token exchange still intersects requested
scopes with the introspected subject's actual authority; an additional recognized
name cannot mint itself. CP's storage/config grants and backfills are separate.

## Rolling release and verification plan

This PR deploys nothing. Normal review and merge queue remain authoritative.
After merge, the release train may carry the new binary, while CP's coherent pin
must name this landed reliant commit and the same Forge SHA. Do not substitute a
PR head or local replace. Storage/config feature PRs stay draft until their real
pins and server contracts pass their own gates.

The database expansion is safe before the new binary: old readers/writers still
accept their existing rows and the old schema vocabulary is a subset. However an
old token parser can fail closed on a newly minted scope. Until all serving roles
are upgraded, do not enable new scope mints/grants or storage/config UI entry
points. A refusal must remain a permission/unsupported-scope response, not fallback
to broader authority. No existing token is rewritten by these migrations.

For a future authorized rollout, verify in the target environment after release:
1. Existing relying API token still succeeds on the same ordinary request.
2. Narrow storage/config-only credential is refused on product API RPCs and token
   exchange cannot add an unheld scope; no bearer or token value in logs.
3. Observe no CHECK-violation/token-parse spike during role replacement. Run the
   normal Forge observability release check and act on its exit code; do not call
   the release healthy from pod readiness alone.
4. Only after all roles converge may separately reviewed CP grants/features mint
   the new scopes. Test an explicitly granted user and an ordinary ungranted
   member; the latter must remain denied.

No production query, deployment, grant, migration execution against existing user
DBs, bucket opt-in or rollout is performed as part of the local proof.
