---
name: migrations/v1.8.0/profile-guided-builds
description: Profile-guided builds in an existing project (forge v1.8.0). (1) The local profiles schema gains profile_pgo_edges and its materialized view, the per-hour CPU call-site edges `forge pgo pull` turns into cmd/<bin>/default.pgo. deploy/observability/profiles-schema.sql is yours, so `forge project upgrade` shows the new statements but does not write them; add them (or force the file when you never edited it), then `forge env up`. (2) pkg/observe now keeps inlined frames in pprof order (inlinee first) and attaches forge.profile.start_lines / forge.profile.leaf_frames to CPU samples; services pick this up by building against forge/pkg v1.8.0. Use when deploy/observability/profiles-schema.sql exists and does not mention profile_pgo_edges.
version: v1.8.0
detection: f=deploy/observability/profiles-schema.sql; test -f "$f" && ! grep -qs 'profile_pgo_edges' "$f"
---

# Profile-guided builds in an existing project (v1.8.0)

## 1. The local profiles schema gains `profile_pgo_edges`

**Use when** `deploy/observability/profiles-schema.sql` exists and does not
mention `profile_pgo_edges`.

### What changed

Profile-guided builds (`forge pgo pull`) read call-site edge weights from a new
30-day table in the local ClickStack, `default.profile_pgo_edges`, filled by
`default.profile_pgo_edges_mv` from the raw `otel_profiles` rows, per hour and
per project (`service.namespace`), service, env and version. Go's PGO keys
an edge on (caller, line minus the caller's start line, callee), and the
collector's ClickHouse exporter does not store start lines, so pkg/observe
sends them as a CPU sample attribute and the view counts edges the way Go's
own profile graph does.

`profiles-schema.sql` is scaffolded once and is yours, so `forge project
upgrade` lists it as user-modified, prints the two missing statements in its
diff, and leaves the file alone. Until you add them, `forge pgo pull` finds no
local edges; nothing else changes.

### What to do

1. Add the statements:
   - **You never edited the file:** `forge project upgrade --force deploy/observability/profiles-schema.sql`
     rewrites it from the template.
   - **You did:** append the `CREATE TABLE IF NOT EXISTS default.profile_pgo_edges`
     and `CREATE MATERIALIZED VIEW IF NOT EXISTS default.profile_pgo_edges_mv`
     statements from `forge project upgrade --dry-run`'s diff (or from a fresh
     `forge project new`) to the end of your copy, unchanged.
2. `forge env up dev`. The `profiles-schema` job re-applies the file on every
   up; every statement is `IF NOT EXISTS`, so the existing tables and their
   data are untouched.
3. Check: `forge env up` succeeds, and after a service has sent a CPU profile,
   `SELECT count() FROM default.profile_pgo_edges` in the ClickStack's
   ClickHouse is non-zero.

Edges accrue only from CPU samples that carry the new attributes (section 2),
and only from the moment the view exists: the view does not backfill the 24
hours of raw rows already stored.

Hosted environments need nothing: the control plane creates the same table
for every tenant.

## 2. CPU profiles carry start lines, and inlined frames keep their order

**Use when** a service imports `github.com/reliant-labs/forge/pkg/observe` (any
forge service does, through serverkit).

### What changed

- Every CPU sample pkg/observe exports carries `forge.profile.start_lines` (each
  frame's function start line, aligned with the stored `FunctionNames`) and
  `forge.profile.leaf_frames` (how many frames belong to the stack's two
  leaf-most locations). Heap and other profiles are unchanged. Cost: about 95
  bytes per stored CPU row.
- The OTLP translator sorted each location's lines by line number, which put an
  inlined function and the caller it was inlined into in arbitrary order in
  every stored stack and flamegraph. pkg/observe now restores pprof's order
  (inlinee first). A stack with inlined frames therefore gets a new
  `StackHash` once: a range flamegraph spanning the upgrade can show the old
  and the corrected variant side by side until the old one ages out of the
  30-day aggregates.

### What to do

Nothing beyond building your services against forge/pkg v1.8.0 (the version
`forge project upgrade` pins). A service still running an older pkg/observe
keeps working and contributes no PGO edges.
