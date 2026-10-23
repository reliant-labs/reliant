---
name: migration-cli
description: Migrate a CLI / library project to forge — --kind cli, second binaries, when contract.go isn't worth it.
---

# Migrate a CLI or Library Project

Use this skill when the existing project is a Cobra CLI binary, a code generator, an ops tool, or a pure Go library. For network-facing apps see `migration-service`. For prerequisites and the overall flow see `migration`.

**A CLI/binary is not a wiring-free special case.** It uses the SAME paved path as a server: proto-driven typed `internal/config`, the cmdkit lib (DB open, logger, flag/env binding, report envelope), and `obs`-injected logging. Do NOT hand-roll `os.Getenv`, ad-hoc `slog.Logger`s, hardcoded timeouts, or hand-rolled shutdown — those are the symptoms of a missing paved path, not a CLI convention. `contract.go` is optional for a trivial CLI; the config/logger/shutdown paved path is not.

## Scaffold

```bash
forge project new <name>-next --kind cli --mod github.com/<owner>/<name>-next
```

`--kind cli` skips the auto-emitted Connect-RPC service, service protos, `deploy/`, and KCL manifests. It does NOT skip config or wiring — config and the composition are app code regardless of kind. You get:

```
cmd/<name>-next/main.go    # Cobra root command
internal/                  # DEFAULT HOME for your packages
pkg/config/                # typed config — generated from proto config blocks
internal/app/              # composition (providers.go + compose.go, both yours)
forge.yaml                 # Project config (strictly top-level)
go.work + go.mod           # Workspace + module
```

For a pure library (no `cmd/` at all), use `--kind library`. Same skeleton minus the binary entrypoint.

## Where the entrypoint lives

`cmd/<name>-next/main.go` is the Cobra root. The display binary name is whatever `cmd/<dir>/` is named — at final cutover, rename `cmd/<name>-next/` → `cmd/<name>/` to drop the `-next` suffix from the installed binary.

The Cobra `Use:` field inside `internal/cli/root.go` (or wherever your CLI source lives) is independent of the binary name. Forge's own pattern is `Use: "forge"` even though the migration ships as `forge-next`. Adjust at cutover or leave — most users only see the binary name.

## Config — proto-driven, not `os.Getenv`

A CLI's settings live in a `<Component>Config` proto message annotated with `(forge.v1.config)`, projected into typed `internal/config` and bound via the cmdkit flag/env path. Scalars (`string`/`int`/`bool`/`Duration`, including timeouts) are config — they go in the config block, consumed as one typed `Cfg config.<Component>Config` field. Do not scatter raw `os.Getenv("FOO")` with magic strings or duplicate the same timeout constant across commands. `forge.yaml` stays strictly top-level (identity, features); per-env config lives in `deploy/kcl/<env>/` for deployed binaries.

## Composition — owned providers, generated wiring, even for a CLI

A binary still has a small typed composition for whatever it constructs: the owned `Infra` provider set + `OpenInfra` in `internal/app/providers.go`, plus `NewComponents(infra *Infra) (*Components, error)` in the owned `internal/app/compose.go` that builds the dependency closure in type-topological order, handing each component its `Deps` as interface-typed fields resolved by type off `infra.<Field>`, never by string name. For a one-package CLI this is a few lines; the point is that the logger, DB handle, and config flow through one owned place (`Infra`) — not a fresh ad-hoc logger per command. There is NO string-keyed registry and NO name-matched wiring.

## Adding a second binary

Use `forge scaffold binary <name>`. See the `binaries` skill for the full lifecycle; the short version:

```bash
forge scaffold binary <second-binary>
```

This scaffolds `cmd/<name>.go` (a real, owned Cobra subcommand on the shared root) plus `internal/<name>/{contract.go,<name>.go,<name>_test.go}` and appends a `binaries:` entry to `forge.yaml`. The subcommand is a Go symbol you can jump to with its own flags/help — not a string projected through a registry.

For an admin / inspector CLI that hosts its own child subcommands (e.g. `<binary> dump-state`, `<binary> reset-foo`), `forge scaffold binary` is still the right starting point: the scaffolded `<name>Cmd` Cobra command happily hosts both its own `RunE` body and child commands registered via `<name>Cmd.AddCommand(...)`. Add child subcommands as sibling files under `cmd/<name>_<subcmd>.go`. The `workaround-cmd-not-in-binaries` lint can fire on those sibling files in current forge — a known false-positive on subcommand-glue files; treat it as advisory.

## Pure-utility packages — three options

For packages like `naming`, `validate`, format helpers — pure functions on no state — `forge package new` is overkill. The contract+mock generation has no test seam to mock when the package has no I/O, no time, no randomness, no external state.

Three honest options:

### (A) Skip the contract
Set in `forge.yaml`:
```yaml
contracts:
  strict: true
  allow_exported_funcs: true
  exclude:
    - internal/<utility-pkg>
```
Honest about the lack of test seam. Best for string-case conversions, pure formatters.

### (B) Wrap in a `Service` struct
Expose functions as methods on a `Service` struct, define a `Service` interface in `contract.go`. Now mockable, but verbose at every call site (`naming.New().ToPascalCase(s)` instead of `naming.ToPascalCase(s)`).

### (C) Hybrid
Keep free functions for ergonomics, ALSO expose a `Service` interface that delegates to them. Best of both, slightly redundant. Pick this when consumers want to mock but you don't want to break call sites.

**Decision rule:** pure utilities with no I/O / time / randomness / external state → (A). Anything that touches one of those → (B) or (C). For the full pattern (interface design, generated `*_gen.go`, test usage), see `contracts`.

## Porting CLI commands

CLI commands map to `internal/cli/` (or wherever your Cobra commands live). The `--kind cli` scaffold doesn't pre-emit a command tree — it gives you a Cobra root in `cmd/<binary>/main.go` and you build out from there. Recommended: one Cobra command per file under `internal/cli/<command>.go`, mirroring the source layout. Set the contracts floor before porting:

```yaml
contracts:
  strict: true
  allow_exported_vars: false
  allow_exported_funcs: false
```

Cobra commands themselves don't need `contract.go` (they are wired into the root cmd, not consumed by other packages); the rule applies to the `internal/<utility>/` and `internal/<service>/` packages they call into.

## When `forge generate` is mostly a no-op

For `--kind cli`, `forge generate` does NOT regenerate Connect-RPC stubs or frontend hooks (none exist). It DOES re-project `internal/config` from your proto config blocks and produce `mock_gen.go` for any `internal/<name>/contract.go` it finds. (Observability lives in the `forge/pkg/observe` interceptor/helper layer, not per-package `*_gen.go` wrappers.)

Run `forge generate` after every config-block or contract-bearing package port. Skipping it means stale mocks/config; tests against your interface compile against the previous shape.

## Sub-module under `pkg/`

`pkg/` is ONLY for code with real external importers you will support as public API. No external importer ⇒ keep it in `internal/`, not `pkg/`. Carve out a `pkg/` sub-module (its own `pkg/go.mod`) only when you have **runtime libraries** downstream consumers should import without pulling in your CLI / build-tool dep graph (Cobra, codegen libs, Delve, etc.). Symptoms:

- Tooling deps bleeding into `go.sum` of importers.
- Wanting independent versioning for the runtime surface.

Setup:
```bash
# Drop a fresh go.mod at pkg/
cd pkg && go mod init github.com/<owner>/<name>-next/pkg
# Copy only the deps the runtime libraries actually use (don't inherit
# the parent's entire require block).
```

If the parent already has `go.work`, add `pkg` to the `use (...)` block. Otherwise put `replace github.com/<owner>/<name>-next/pkg => ./pkg` in the parent `go.mod`. **Don't do both** — workspace `use` supersedes `replace` and they're equivalent for build resolution.

Every directory physically under `pkg/` joins the sub-module the moment `pkg/go.mod` exists. If a sub-package needs to stay in the main module, move it out of `pkg/` BEFORE introducing the sub-module — there's no clean way to exclude a subdir.

## Final checks before declaring done

```bash
forge generate          # config + mocks for any contract.go
forge lint              # contract + general lints
go build ./...          # main module + sub-modules in the workspace
go test ./...
go install ./cmd/<name>-next
$(go env GOPATH)/bin/<name>-next --help    # smoke
```

## Rules

- Use `--kind cli` (or `--kind library`) at scaffold time. Don't disable server-shaped emission post-hoc.
- CLI/binary kinds use the SAME typed `internal/config` + cmdkit paved path as servers. No raw `os.Getenv`, ad-hoc loggers, or hardcoded timeouts.
- App code lives under `internal/`. `pkg/` is only for code with real external importers.
- `contract.go` is optional for a trivial CLI; the config/logger/shutdown paved path is not.
- Use `forge scaffold binary <name>` for second binaries (real owned Cobra subcommands; see `binaries`). The scaffolded command also hosts child subcommands via `<name>Cmd.AddCommand(...)`.
- For pure-utility packages, pick option (A), (B), or (C) explicitly. Don't blanket-apply `forge package new`.
- `pkg/` sub-module: workspace `use` OR `replace`, not both.

## When this skill is not enough

- **Server-shaped projects** — see `migration-service`.
- **Designing the interface in `contract.go`** for non-trivial packages — see `contracts`.
- **Pre-flight, module path strategy, what to do when forge itself is wrong** — see `migration`.
