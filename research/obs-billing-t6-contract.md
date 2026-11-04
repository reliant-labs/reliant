# Observability billing UI (T6)

## Contract dependency

The original export used control-plane `feat/obs-billing-service` at
`60f4c1737534d2c90a4dddc2a040f006bc91f862`. Recovery rebases the public contract
onto control-plane main `f31420e5ddad8b2d4a40a974ba532fdd7fcdd4e8`, preserving
its billing cost-summary and observability query-quota contracts. The canonical
input union is pinned locally at `c153f050fdcefd20b3afc31e526336825bad1348`:
main plus the original branch's four public proto/allowlist changes (relative to
`297d9ba34fffb93d91ac3f8083d6b246db788e12`), applied with `git apply --3way`.
No generated/vendor files were hand-merged. Export with
`CONTROL_PLANE_DIR=<that checkout> node .github/scripts/sync-controlplane-proto.mjs`,
then run `task generate:controlplane` with the repository's pinned plugins and Go
toolchain. `--check --require` verifies the 32-file export against that union.
The local canonical-input commit is not a new backend PR and was not pushed.

This PR still depends on T2 (control-plane #1398) landing. A drift check against
unmodified control-plane main remains blocked until T2 merges. Deployment is separate: an
Unimplemented management read shows an explicit unavailable message and the
existing usage read as a labelled, read-only rollout fallback. Permission errors
never use that fallback. No failed read becomes zero usage.

## Contract constraints represented honestly

- ListObservabilityUsage returns exact bytes and rows, but no per-bucket dollar
  allocation. The table and CSV report those exact quantities, with actual dollars
  only at org/month level. They do not invent service invoice shares.
- Query lane `usage_known=false` means unavailable, not zero.
- Stored-now has its own snapshot timestamp; missing snapshots are not zero.
- Alert threshold preferences can be saved; the API has no recipient linkage or
  delivery-capability field. The UI explicitly says delivery is not connected,
  and does not pretend saving thresholds enables any receiver.
- Collection policy reads DeployService.ListEnvironments declared release shapes,
  using the existing shape decoder. Missing replay/sampling details are labelled
  unreported, not inferred. No new collection-policy endpoint.
- Update uses the version read when editing began (including default version 0).
  Aborted blocks the draft until explicit reload and review. No mutation retries.
  The backend is authoritative for validation and the already-charged floor.

## Preview

Dev-only existing route:
`/forge-observability-preview?billingCase=happy` (also `empty`, `conflict`,
`member`, `unavailable`). These are prop-driven fixtures; no live billing writes.
The existing observability preview retains a deliberate Unimplemented fixture
for the legacy usage fallback.
