---
name: audit-json
description: Reading `forge project audit --json` and `forge project map --json` — the JSON shapes, common jq queries, CI integration patterns, and the additive-extension contract that keeps consumers stable as new finding types appear.
---

# Audit + Map JSON Output

Machine-readable counterparts to the human-formatted `forge project audit` /
`forge project map` (same data, ANSI colours + tree characters). Use JSON for CI
gates, dashboards, sub-agent introspection, scripted audits. New finding types are
added additively — existing keys never change shape.

- **`forge project audit --json`** — per-category roll-up of project health:
  forge version pin, project shape, lint status, codegen drift, migration
  safety, scaffold markers, deps. One JSON object per run.
- **`forge project map --json`** — annotated project tree: every file labelled
  user-owned, forge-space (regenerated), scaffold-with-markers, or
  drifted (forge-space file with hand-edits). One nested-tree JSON
  object per run.

## `forge project audit --json` shape

```jsonc
{
  "project_name": "myproject",
  "project_kind": "service",
  "binary_version": "0.7.2",
  "generated_at": "2026-05-07T15:06:12.407Z",
  "categories": {
    "version": { "status": "ok",   "summary": "...", "details": { ... } },
    "shape":   { "status": "ok",   "summary": "...", "details": { ... } },
    "conventions": { "status": "warn", "summary": "...", "details": { ... } },
    "codegen": { "status": "warn", "summary": "...", "details": { ... } },
    "file_sizes": { "status": "ok", "summary": "...", "details": { ... } },
    "migration_safety": { "status": "ok", "summary": "...", "details": { ... } },
    "scaffold_markers":  { "status": "ok", "summary": "...", "details": { ... } },
    "unscoped_auth": { "status": "warn", "summary": "...", "details": { ... } },
    "deps":    { "status": "ok",   "summary": "...", "details": { ... } }
  },
  "overall_status": "warn"
}
```

Top-level keys are stable:

| Key | Type | Meaning |
|-----|------|---------|
| `project_name` | string | `name:` from forge.yaml, or directory basename if no forge.yaml |
| `project_kind` | string | `service`, `cli`, `library`, or `unknown` |
| `binary_version` | string | The forge binary that produced the audit (`dev` for local builds) |
| `generated_at` | RFC3339 timestamp (UTC) | When the audit ran |
| `categories` | object | Per-category roll-ups, keyed by category name |
| `overall_status` | string | `ok` / `warn` / `error` — worst category status wins |

Each category is the same shape:

```jsonc
{
  "status":  "ok" | "warn" | "error",
  "summary": "human-readable one-liner",
  "details": { ... category-specific keys ... }   // optional
}
```

### Category keys

`ingress` appears only when the repo declares a `forge.Gateway` in `deploy/kcl` and
`prerequisites` only for deploy-shaped projects; the rest are always emitted.
Iterate `.categories | keys[]` rather than hard-coding the list.

| Category | What's in `details` (when relevant) |
|----------|-------------------------------------|
| `version` | `pinned_version`, `binary_version`, `ci_pin`, `state_pin`, `intentional`, `hint` (when mismatch) |
| `shape` | `services[]`, `frontends[]` (each `{name, type}`), `packages[]`, `proto_integrity` (only when proto parsing failed). Workers and operators are owned code with no proto contract — forge does **not** inventory them. |
| `shape.services[]` | `name`, `type`, `rpc_count`, `rpcs[]` — each rpc is `{name, streaming?, custom?}`. `streaming` is omitted for unary RPCs, else `"client"`/`"server"`/`"bidi"`; `custom: true` marks an rpc declared `// forge:custom` (stubbed, never CRUD-wired, whatever its name). |
| `features` | `resolved{}` (every feature → bool), `enabled[]`, `disabled[]`. Features are derived from the repo, never configured. Informational — always `ok`. |
| `ingress` | `gateways`, `http_routes`, `grpc_routes`, `services_without_route`, `findings[]` |
| `environments` | `environments[]` (each `{name, status}` — one per `deploy/kcl/<env>/main.k`) |
| `external_builds` | `enabled`, `services[]` |
| `prerequisites` | `external_secrets`, `dns_records`, `byte_match_groups`, `undeclared_secret_mounts`, `findings[]` |
| `conventions` | `counts{}` (per-rule violation counts), `hint` |
| `codegen` | `tracked_files` / `certified_files` (same count), `last_generate`, `legacy_manifest`, `user_edited_gen_files[]`, `orphan_gen_files[]`, `disowned_files[]` (`{path, since, reason}`) + `disowned_hint`, and `forked_files[]` (**legacy**: always empty, kept only so a consumer reading the key does not break — see `forked_files_note`) |
| `migration_safety` | `migration_count`, `migrations_dir`, `latest_migration`, `latest_migration_mtime`, `allow_destructive_files[]` (migrations carrying `-- forge:allow-destructive`), `destructive_change_severity`, `hint`. `warn` when any migration carries the directive. |
| `optional_deps_guard` | `finding_count`, `affected_packages[]`, `by_package{}`, `hint` (unguarded derefs of `// forge:optional-dep` Deps fields — warn-level; run `forge lint --optional-deps-guard` for per-line detail) |
| `config_deps` | same rollup shape as `optional_deps_guard` — scalar Deps fields, which wire can never resolve; declare a component config block in `proto/<project>/config/v1` instead |
| `scaffold_markers` | `total_markers`, `files[]` (paths still carrying `FORGE_SCAFFOLD:` lines) |
| `crud_stubs` | `files[]`, `total_stubs`, `stubs[]` (`{file, method, reason}`), `marker`, `legacy_marker` — `forge:custom-read-shape` stubs in the user-owned `internal/handlers/<svc>/handlers_crud.go` (the pre-rename `FORGE_CRUD_SHAPE_MISMATCH` marker stays recognized for one release). A stub marks a deliberate non-AIP-158 read shape the user must implement; each stubbed RPC returns `CodeUnimplemented`, so the category is `warn` whenever `total_stubs > 0`. |
| `unscoped_auth` | `authenticated_rpcs`, `scoped_rpcs`, `unscoped_rpcs[]` (`{service, method, file, delegating, entity, table}`), `owner_scoped_unscoped_rpcs[]` (the GATING subset — same shape, always carrying `entity`/`table`), `owner_scoped_tables[]`, `owner_marker`, `acknowledged_rpcs[]` (`{service, method, file, reason}`), `auth_seam`, `caller_resolvers[]` (`<import path>.<Func>` for each function the project marked `//forge:resolves-caller`), `caller_resolver_marker`, `caller_resolver_markers_ignored[]` (`{file, line, reason}`, present only when a marker sits on a method), `acknowledge_marker`, `hint` — RPCs whose proto declares `auth_required: true` but whose handler body never resolves the caller, so every signed-in user reaches every row. The authenticated set is read from `gen/forge_descriptor/` (the same `auth_required` projection that drives `pkg/middleware/procedures_gen.go`); the reads-the-caller set is the handler AST, matched against the scaffolded auth seam and any `//forge:resolves-caller` function the project declared; the owned-table set is read from the migrations' `COMMENT ON COLUMN ... IS 'forge:owner'` declarations. **Status is `warn` when `unscoped_rpcs` is non-empty, and `error` when `owner_scoped_unscoped_rpcs` is** — a project that declares no owner column stays advisory (a fresh scaffold is unscoped by construction), while one that declared where its rows' owner lives fails the gate for the RPCs crossing that boundary. The RPC→table join is the CRUD-name derivation, so a custom (non-CRUD) RPC cannot arm the gate however many owned tables it touches; it still reports as a warning. An intentionally global RPC is declared IN CODE above the handler with `// forge:auth-unscoped-ok: <reason>` — this suppresses the error as well as the warning; the reason is required and is echoed back in `acknowledged_rpcs` for review. If the project declares authenticated RPCs and NO handler matched any of them, the category reports `warn` saying it inspected nothing rather than a vacuous `ok`. |
| `file_sizes` | `line_threshold`, `method_threshold`, `oversized_files[]` (`{path, lines}`), `god_object_types[]` (`{path, type, methods}`), `advisory: true`. **Status is always `ok` — it never gates `overall_status`.** |
| `orphan_stubs` | `orphan_services[]` (`{service, dir, stub_methods[]}` — every handler method still carries `// forge:gen unwired-stub`, so the service serves 501s), `hint`. Fix by implementing the handlers or `forge project delete service <name>`. |
| `unhandled_rpcs` | `unhandled_rpcs[]` (`{service, rpc, dir}`), `unresolved_services[]` (only when some `Service` struct embeds a type whose promoted methods the scan cannot see), `hint` — rpcs of MANAGED services whose handler package declares no `*Service` method. The embedded `Unimplemented<Svc>Handler` makes each one compile and serve `CodeUnimplemented` with no stub saying so, invisible to `orphan_stubs` and `shapes`. Every forge path gives an rpc a method (a stub for a custom rpc, a shim for a CRUD one), so a hit means generate has not run since the proto changed or a hand edit dropped the method. `warn` whenever the list is non-empty. |
| `deps` | `go_mod`, `go_sum`, `gen_go_mod` presence flags |

### Status semantics

| Status | When |
|--------|------|
| `ok` | Category looks healthy. |
| `warn` | Soft drift — fixable without blocking work. Codegen orphans, unresolved wire fields, unguarded optional-dep derefs, orphan/CRUD stubs, newer-binary version mismatch. |
| `error` | Hard problem — build will fail or behaviour is broken. Missing forge.yaml, error-severity convention violations, unwired scaffolds under strict wiring, undeclared secret mounts. |

`overall_status` is the worst category status. CI gates that block on
`error` (and only `error`) hit the right balance for most projects.

## `forge project map --json` shape

```jsonc
{
  "path": ".",
  "name": "myproject/",
  "is_dir": true,
  "children": [
    {
      "path": "internal/handlers/users/handlers_crud_gen.go",
      "name": "handlers_crud_gen.go",
      "is_dir": false,
      "ownership": "forge-space, hand-edited (drift from regen)",
      "flags": ["drift"]
    }
  ]
}
```

Each `MapNode` carries:

| Key | Type | Meaning |
|-----|------|---------|
| `path` | string | Path relative to the project root (forward slashes) |
| `name` | string | Display name (trailing `/` for directories) |
| `is_dir` | bool | Directory vs file |
| `ownership` | string (optional) | One of the ownership classes below |
| `flags` | string[] (optional) | Health flags — `drift`, `FORGE_SCAFFOLD`, `diverged-from-migrations` |
| `children` | []MapNode (optional) | Subdirectory contents |

The output is the annotated tree and nothing else — the four keys above are the
whole document.

### Ownership classes

| Value | Meaning |
|-------|---------|
| `user-owned` | Hand-written code; forge never touches it. |
| `forge-space, regenerated` | Tier-1 codegen; rewritten every `forge generate`. |
| `forge-space, hand-edited (drift from regen)` | Tier-1 file whose checksum no longer matches the generator output. Flagged with `drift`. |
| `scaffold, FORGE_SCAFFOLD markers present` | Tier-2 scaffold with at least one `FORGE_SCAFFOLD:` line still present — not yet customised. |

`flags` adds machine-greppable health hints orthogonal to ownership: `drift`
(Tier-1 file with hand-edits), `FORGE_SCAFFOLD` (placeholder markers still
present), and `diverged-from-migrations` (a `proto/db/` entity whose shape
disagrees with the migrations that own the schema).

## Common queries

```bash
# Fail CI on any error-severity category.
forge project audit --json | jq -e '.overall_status == "error" | not' \
  || (echo "forge project audit found errors"; exit 1)

# List every hand-edited generated file.
forge project audit --json | jq -r '.categories.codegen.details.user_edited_gen_files[]?'

# Count Tier-1 files (forge-regenerated).
forge project map --json | jq '[.. | select(.ownership? == "forge-space, regenerated")] | length'

# List every drifted file with its path.
forge project map --json | jq -r '.. | select(.flags? // [] | index("drift")) | .path'

# List every scaffold still carrying FORGE_SCAFFOLD markers.
forge project map --json | jq -r '.. | select(.flags? // [] | index("FORGE_SCAFFOLD")) | .path'

# Project shape: how many services and frontends?
forge project audit --json | jq '.categories.shape.details |
  {services: (.services | length // 0),
   frontends:(.frontends | length // 0)}'

# Which RPCs are streaming, and which are unary?
forge project audit --json | jq -r '.categories.shape.details.services[]?
  | .name as $svc | .rpcs[]?
  | "\($svc).\(.name)\(if .streaming then " (streaming: \(.streaming))" else " (unary)" end)"'

# Version health: is the project's pinned forge version behind the binary in hand?
forge project audit --json | jq -r '.categories.version.details
  | select(.hint) | "\(.pinned_version) -> \(.binary_version): \(.hint)"'

# Convention violations by severity.
forge project audit --json | jq '.categories.conventions.details.counts'

# Orphan _gen files (sources removed, file forgotten).
forge project audit --json | jq -r '.categories.codegen.details.orphan_gen_files[]?'

# Orphan stubs (services whose every handler is still an unwired stub).
forge project audit --json | jq -r '.categories.orphan_stubs.details.orphan_services[]? | .service'

# Monster files / god-object types over threshold (advisory).
forge project audit --json | jq -r '.categories.file_sizes.details |
  (.oversized_files[]?  | "file \(.path): \(.lines) lines"),
  (.god_object_types[]? | "type \(.path) \(.type): \(.methods) methods")'
```

## CI integration

Load the `audit-json/ci-workflow` skill for the drop-in GitHub Actions workflow
(runs both commands, uploads JSON artifacts, fails on `error`, posts a PR summary).

## Sub-agent patterns

Orient before scaffolding (`.categories.shape.details`, `.project_kind`); check
`user_edited_gen_files[]` before regenerating (regen clobbers hand-edits); check
ownership before deleting a directory:

```bash
forge project map --json | jq -r --arg p "internal/handlers/things" \
  '.. | select(.path? == $p) | .ownership'
```

## Extending

The JSON shape is **additive**:

- Existing keys in `categories`, top-level fields, and per-`MapNode` fields keep
  their meaning across forge releases.
- New categories may appear; iterate `.categories | keys[]`, never assume a closed set.
- New `details` keys may appear; `?` every nested lookup so a missing key is `null`,
  not an error.
- New `flags` values and (rarely) new ownership-class strings may appear; treat both
  as open sets and default-treat unknown values.

The status enum (`ok` / `warn` / `error`) and the `status / summary / details`
per-category shape are **frozen**. A new finding type that doesn't fit an existing
category gets a new category (forge issue or PR) — never shoehorn it into an
unrelated one.

## Rules

- `forge project audit --json` for category roll-ups; `forge project map --json` for
  per-file ownership. Both run from the project root (`audit` resolves forge.yaml,
  `map` walks the file tree).
- Gate CI on `overall_status == "error"`; `warn` is informational.
- Pipe through `jq`. Never grep the prose output — it is not a stable interface.

## When this skill is not enough

- **What `forge generate` is doing under the hood** — see
  `architecture`.
- **CI workflow generation** (where the audit workflow plugs in) —
  see `ci`.
- **Tier-1 vs Tier-2 banner classification** — see `architecture`
  ("Three precise classes").
- **Drift remediation** (regenerate vs accept hand-edits) — see
  `migration-upgrade`.
