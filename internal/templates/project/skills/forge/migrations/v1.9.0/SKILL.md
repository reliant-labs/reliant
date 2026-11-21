---
name: v1.9.0
description: Move a project off what forge deprecated (forge is semver at major 1, so sections 1-5 and the codemods of 9 still work, deprecated, until a v2; 6-8 and the ledgers of 9 are already in force). Deprecated forge/pkg APIs (the pkg/approval package, the root pkg/appkit aliases, devidp's ProofLocker family, observe.ForensicIDName/ForensicSecretIDName, obsquery.IssueEventFilterSQL/ActiveEvidence/ServerCulprit, release.ShapeDiff.KindChanged, contractkit's LogCall/TraceStart/Metrics helpers, the forgepb entity/field annotation types); the retired (forge.v1.entity)/(forge.v1.field) proto options no longer compile; removed CLI flags (lint --crud-fixtures, build --push/--release, scaffold entity --from-schema, storage worktrees --reliant-managed), the hidden forge test stub and the FORGE_NO_HOOKS alias; retired forge.yaml keys now fail the load; KCL ObservabilitySignals.replay, the OTEL_REPLAY build-variable carry and IdentitySocialProvider are gone; root_gen.go is no longer generated; every lint rule's grace window ended (forge-proto-format, forgeconv-unproven-signup gate now); the web runtime's `attribution` option is gone; the tx-isolation and harness-mocks codemods and the old .forge/hashes.json and .forge/scaffolded.json ledgers are no longer read. Use when Go code, a script, forge.yaml, deploy/kcl or a frontend still names one of them.
version: v1.9.0
detection: grep -rqsE --include='*.go' --exclude-dir=gen --exclude-dir=vendor --exclude-dir=node_modules '"github.com/reliant-labs/forge/pkg/(approval|appkit)"|^[^/]*\b(contractkit\.(LogCall|LogCallErr|TraceStart|RecordSpanError|NewMetrics|Metrics)|devidp\.(ProofLocker|UnavailableProofLocker|NewPostgresProofLocker|PostgresProofLocker|PostgresProofLockerOptions|ErrProofLockBusy|DefaultProofLockWait|DefaultProofLockHold|MetaProofLock|MetaProofWindowPrefix)|observe\.Forensic(Secret)?IDName|obsquery\.(IssueEventFilterSQL|ActiveEvidence|ServerCulprit)|release\.KindChange|forgepb\.(EntityOptions|FieldOptions|IndexDef|StoreAs|ValidationRules|E_Entity|E_Field))\b|\bProofLocker:|\.KindChanged\b' . && exit 0; grep -rqsE --include='*.proto' --exclude-dir=gen --exclude-dir=vendor --exclude-dir=node_modules '\(forge\.v1\.(entity|field)\)' proto && exit 0; grep -rqsE --exclude-dir=gen --exclude-dir=vendor --exclude-dir=node_modules --exclude-dir=.git -- '--crud-fixtures|--reliant-managed|--from-schema|\bFORGE_NO_HOOKS\b|forge build[^\n]*--(push|release)\b' Taskfile.yml Makefile scripts .github cmd internal && exit 0; grep -qsE '^(environments|auth|version|hot_reload|packs|pack_overrides|kind|components|services|packages|binaries|test|docs):' forge.yaml && exit 0; grep -rqsE --include='*.k' '(ObservabilitySignals|signals[^=\n]*\|)[[:space:]]*\{[^}\n]*\breplay[[:space:]]*=|IdentitySocialProvider|(NEXT_PUBLIC_|VITE_)OTEL_REPLAY' deploy/kcl && exit 0; for f in cmd/*/cmd/root.go; do test -f "$f" && grep -qs 'generatedCommands(' "$f" && exit 0; done; grep -rqsE --include='*.ts' --include='*.tsx' --exclude-dir=node_modules '\battribution:[[:space:]]*(true|false)\b' frontends src && exit 0; test -f .forge/hashes.json && exit 0; test -f .forge/scaffolded.json && exit 0; exit 1
---

# Upgrading to v1.9.0

This release was first cut to delete what earlier releases deprecated. forge
is semver at major 1, so that deletion was walked back before it shipped:
what sections 1 to 5 name, and the two codemods of section 9, still compile,
load and run, as deprecated aliases that warn and name their replacement.
They are removed only in a future major. Moving off them now is still the
right change, and each section says how. Sections 6 to 8 and the ledger
formats of section 9 are already in force. Every section is independent:
apply the ones whose "Use when" matches, then run `forge generate && forge
lint && go build ./...`.

## 1. Deprecated forge/pkg APIs

**Use when** `go build ./...` fails on one of the names below after the pin
bump.

### What changed

| Deprecated | Use instead |
|---|---|
| package `forge/pkg/approval` (every symbol) | your own copy: it never had a forge caller. Copy the package from the module cache of the release you pin today (`$(go env GOMODCACHE)/github.com/reliant-labs/forge@<old version>/pkg/approval`) into your module and change the import |
| package `forge/pkg/appkit` (root: `WorkerInstance`, `ContextWorkerInstance`, `WorkerLifecycle`, `NewWorkerInstance`, `NewContextWorkerInstance`, `WrapWorker`) | `forge/pkg/lifecyclekit`, same names. `forge/pkg/appkit/operatorkit` stays |
| `devidp.ProofLocker`, `UnavailableProofLocker`, `NewPostgresProofLocker`, `PostgresProofLocker`, `PostgresProofLockerOptions`, `ErrProofLockBusy`, `DefaultProofLockWait`, `DefaultProofLockHold`, `MetaProofLock`, `MetaProofWindowPrefix`, `SignInRoutesConfig.ProofLocker` | nothing: sign-in needs no lock. Delete the field and the locker you built for it |
| `observe.ForensicIDName`, `observe.ForensicSecretIDName` | `observe.ForensicIDValueName`, `observe.ForensicSecretName`, `observe.ForensicSecretNumberName` |
| `obsquery.IssueEventFilterSQL` | `obsquery.IssueRecordFilterSQL` |
| `obsquery.ActiveEvidence` | `obsquery.UserActivityEvidence` |
| `obsquery.ServerCulprit` | `obsquery.ServerIssueCulprit` (`ServerCulpritSQL` is unchanged) |
| `release.ShapeDiff.KindChanged`, `release.KindChange` | `release.ShapeDiff.RuntimeChanges` (`KindChanged` was never set) |
| `contractkit.LogCall`, `LogCallErr`, `TraceStart`, `RecordSpanError`, `Metrics`, `NewMetrics` | `observe.LogCall`, `observe.TraceCall`, `observe.NewCallMetrics` |
| `forgepb.EntityOptions`, `FieldOptions`, `IndexDef`, `StoreAs` (and its values), `ValidationRules`, `E_Entity`, `E_Field` | nothing: the schema is your migrations (section 2) |

### What to do

1. Run `go build ./...` and fix each error with the table.
2. A `contractkit` caller: `observe.LogCall(ctx, logger, method, start, err)`
   logs a finished call (defer it after `start := time.Now()`),
   `observe.TraceCall(ctx, tracer, op, fn)` runs `fn` inside a span and records
   its error, and `observe.NewCallMetrics(meter, namespace)` returns the call
   counters and duration histogram (`forge project libraries observe` prints
   every signature).

## 2. `(forge.v1.entity)` and `(forge.v1.field)` are deprecated

**Use when** a `.proto` under `proto/` still sets `option (forge.v1.entity)`
or a `[(forge.v1.field) = …]` field option.

### What changed

The options were retired long ago and ignored since: the schema is projected
from `db/migrations`. Their extensions stay in `forge/v1/forge.proto` within
major 1 so a file that still sets one compiles; `forge generate` prints a
notice for each, and a v2 removes them.

### What to do

Delete each `option (forge.v1.entity) = {…};` and each
`[(forge.v1.field) = {…}]`. A column, an index, a default or a check you meant
by one belongs in a migration (`forge skill load db`).

## 3. Deprecated CLI flags, commands and environment variables

**Use when** a script, Taskfile, workflow or Go exec site passes one of them.

The top-level `forge build` command is compile-only. Move publishing flags to
`forge env build` and specify the environment.

| Removed | Use instead |
|---|---|
| `--crud-fixtures` (on `forge lint`) | `forge lint --fixture-drift` |
| Top-level build's `--push` / `--release` | `forge env build <env> --push` / `forge env build <env> --release <v>` |
| `--from-schema` (on `forge scaffold entity`, which only refused it) | proto-first birth: `// forge:entity` on the message, then `forge scaffold` |
| `--reliant-managed` (on `forge storage worktrees`, accepted and ignored) | drop the flag |
| the hidden `test` command (it only said "use task test") | `task test` |
| `FORGE_NO_HOOKS=1` | `FORGE_NO_GIT_HOOKS=1` |
| `--port` on `forge debug start` setting the service's `PORT` too | `--port` is Delve's port only; `--app-port` sets the service's |
| `mode=orm` for protoc-gen-forge | nothing: the ORM is generated from the applied schema by `forge generate` |
| `forge project audit --json`: `codegen.forked_files`, `forked_files_note`; the `FORGE_CRUD_SHAPE_MISMATCH` marker | `codegen.disowned_files`; `// forge:custom-read-shape:` in `handlers_crud.go` |

Each still works as it did, hidden from `--help`, and says on stderr what
replaces it (the hidden `forge test` stub excepted: `forge test` is now a
command of its own). Fix the call site before a v2 removes them.

## 4. Retired forge.yaml keys warn

**Use when** `forge.yaml` still has one of `environments`, `auth` (and
`auth.provider` / `auth.jwt` / `auth.api_key`), `version`, `hot_reload`,
`ci.go_version`, `ci.extra_jobs`, `lint.contract`, `contracts.strict`,
`contracts.allow_exported_vars`, `contracts.allow_exported_funcs`, `packs`,
`pack_overrides`, `deploy.provider`, `kind`, `components`, `services`,
`packages`, `binaries`, `test`, `docs`, or
`database.migration_safety.down_files_allowed_until`.

### What changed

These keys load with a warning that names the key and its fix, and are dropped
on the next rewrite of forge.yaml. A v2 makes them fail the load.

### What to do

Run any forge command: each error says what replaced the key. In almost every
case the fix is to delete it (the value is discovered from the code, or lives
in the env's KCL).

## 5. KCL: env-wide replay, the `OTEL_REPLAY` build variable, `IdentitySocialProvider`

**Use when** an env's `deploy/kcl/<env>/main.k` sets `replay` on
`forge.ObservabilitySignals` (directly or by merging `{replay = True}` into
`signals`), an `env_vars` block sets `NEXT_PUBLIC_OTEL_REPLAY` /
`VITE_OTEL_REPLAY`, or KCL names `forge.IdentitySocialProvider`.

### What changed

- `ObservabilitySignals.replay` is deprecated: it still opts the env in.
  Session replay is a per-frontend switch.
- A `{NEXT_PUBLIC_,VITE_}OTEL_REPLAY` build variable is still carried into the
  runtime config (deprecated), but the generated `otel_gen.ts` no longer reads
  it as a build-time fallback.
- `IdentitySocialProvider` (a placeholder `social` already refused) is
  deprecated; it stays declared so a module naming it compiles.

### What to do

1. Delete `replay` from the env's signals, and set it on each frontend that
   records instead:

   ```kcl
   telemetry = forge.BrowserTelemetry {replay = True}
   ```

2. Delete `OTEL_REPLAY` from `env_vars` (the same switch above replaces it).
3. Name `forge.IdentityGoogle` / `forge.IdentityGitHub` in `social`.
4. `forge env render <env>` for each env you changed.

## 6. `cmd/<bin>/cmd/root_gen.go` is a deprecated compatibility file

**Use when** `cmd/<bin>/cmd/root.go` calls `generatedCommands(`.

### What changed

Nothing breaks within major 1: `forge generate` keeps emitting `root_gen.go`
for a `root.go` that still calls `generatedCommands()`, and deletes it once
`root.go` stops. A v2 stops emitting it, so port `root.go` before then.

### What to do

1. `root.go`: add

   ```go
   const ServiceName = "<project>"

   var registeredCommands []Factory

   func registerCommand(f Factory) { registeredCommands = append(registeredCommands, f) }
   ```

   and replace `for _, c := range generatedCommands(deps) { root.AddCommand(c) }`
   with `for _, f := range registeredCommands { root.AddCommand(f(deps)) }`.
2. `db.go`: add `func init() { registerCommand(newDBCmd) }`, and replace
   `migrationSource(), migrationSourceDir` with `forgedb.Source(),
   forgedb.SourceDir` (import `forgedb "<module>/db"`). Do the same at every
   other `migrationSource()` call, `serve.go`'s `migratekit.AutoMigrate`
   included.
3. Delete `root_gen.go` and run `go build ./...`.

## 7. Lint rules that warned now gate

**Use when** `forge lint` turns red after the bump on a rule it used to print
as "new in vX; gates from vY".

### What changed

Every rule's grace window ended: each one in `lint.rules`' rollout table now
gates at its severity. The two that were still warning for a v1.7 pin are
`forge-proto-format` (every bare `forge lint`) and
`forgeconv-unproven-signup`.

### What to do

- `forge-proto-format`: `forge lint --proto-format --fix`, then commit the
  reformatted protos.
- `forgeconv-unproven-signup`: the finding names the env and the fix (deliver
  mail so sign-up proves the address first).
- To take more time on one rule, set it to `warn` in `forge.yaml`'s
  `lint.rules`; that override still wins.

## 8. Web runtime: `attribution` is gone

**Use when** a frontend passes `attribution: true` or `attribution: false` in
its `@reliantlabs/forge-web-runtime` browser telemetry config.

### What changed

`BrowserTelemetryConfig.attribution` was replaced by the analytics tiers.

### What to do

`attribution: false` becomes `analytics: "full"` (the same behaviour: full
mode, no consent until granted). `attribution: true` becomes
`analytics: "full"` plus a call to `grantAnalyticsConsent()` once the person
consents. `enableAttribution({ search })` is unchanged.

## 9. Deprecated codemods and removed ledger formats

**Use when** a runbook calls `forge fix tx-isolation` or
`forge fix harness-mocks`, or the project still has `.forge/hashes.json` or
`.forge/scaffolded.json`.

### What changed

- The two codemods converted v1.0.0 shapes (pkg/orm's explicit transaction
  levels; the test harness's per-interface mocks). Every project inside the
  upgrade window already has both, so they are deprecated: hidden from
  `forge fix --help`, they still run (with a one-line notice), and
  `forge project upgrade` still runs them for a project that needs them.
- `.forge/hashes.json` and `.forge/scaffolded.json` were migrated into
  `.forge/hashes/` and `.forge/scaffolded/` by every forge since v1.0, and
  v1.9.0 no longer reads them.

### What to do

- Delete a runbook step that names either codemod.
- A project that still has either ledger file skipped v1.0.12 through v1.8:
  upgrade through v1.8.0 first (its `forge generate` migrates them), then to
  v1.9.0. A project that has both the file and the directory can delete the
  file.
