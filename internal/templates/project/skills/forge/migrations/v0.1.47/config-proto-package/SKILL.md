---
name: migration-v0.1.47-config-proto-package
description: Upgrade v0.1.47 config protos off the shared package config.v1 — run forge fix config-proto-package, forge generate, then update what it lists. Env var names do not change.
relevance: migration
---

# v0.1.47 — config protos are project-unique

Use when `proto/config/v1/*.proto` declares `package config.v1`. `forge
generate` refuses that layout until it moves.

## Why

Every forge project declared `package config.v1` at
`proto/config/v1/config.proto`. Protobuf registers each file by path and each
message by full name in one process-wide registry, and Go's runtime panics at
init on a duplicate: `file config/v1/config.proto is already registered`. So
two projects' generated config packages could never be linked into one binary
— exactly what pkg/config's generated `Roots()` invites, when another repo imports a
project's `pkg/config` to validate a rendered env with `Roots()` /
`ValidateRootEnv`.

The path and package now carry the project's name — forge.yaml `name`, as a
proto segment (`control-plane` is `control_plane`):

| | before | after |
|---|---|---|
| directory | `proto/config/v1/` | `proto/<project>/config/v1/` |
| package | `config.v1` | `<project>.config.v1` |
| go_package | `<mod>/gen/config/v1;configv1` | `<mod>/gen/<project>/config/v1;configv1` |
| frontend config | `proto/config/v1/<fe>_config.proto` | `proto/<project>/config/v1/<fe>_config.proto` |

Unchanged: every message, field and `(forge.v1.config)` annotation, so **env
var names, flags, `<PROJECT>_CONFIG`, the KCL schemas in `config_gen.k` and
per-env `config.k` values are identical**. The Go package is still named
`configv1`, and `pkg/config`'s exported API (`Config`, the block aliases,
`Load`, `Roots`) does not move.

## Steps

```bash
forge fix config-proto-package --dry-run   # what moves and what is rewritten
forge fix config-proto-package             # needs a clean tree (or --allow-dirty)
forge generate                             # pkg/config, gen/, descriptor, TS stubs
go build ./... && forge lint
```

The codemod moves each `config.v1` file, rewrites its `package` and
`go_package`, rewrites `import "config/v1/…"` and `config.v1.<Message>`
references in every other proto, the `<mod>/gen/config/v1` import in your own
Go (never `gen/` or `*_gen.go`), Go string literals that are exactly the old
package or a name in it (`"config.v1"`, `"config.v1.AppConfig"` — e.g.
`RangeFilesByPackage("config.v1", …)`, which would otherwise silently match
nothing), `gen/config/v1/` imports in frontend
sources, and deletes the stale generated `gen/config/v1/` and
`<fe>/src/gen/config/v1/` trees. It is idempotent: a second run reports
nothing.

It writes nothing and lists the conflict when a destination file already
exists. It keeps a non-default `go_package` and says so.

## After

- Fix each `file:line` it lists under **Next** — docs, CI, `buf.yaml`,
  Taskfile mentions of the old path, and any Go string that only *contains*
  `config.v1` (`"/config.v1.AppConfig"`, a concatenation): the codemod rewrites
  only literals that are exactly the package, so it leaves those for you.
- Another repo that imported `<mod>/gen/config/v1` directly changes that
  import path once it pins this release (its `pkg/config` imports change
  nothing).
- `buf breaking` against a pre-move ref reports the move (file and package
  renames). Expect it once; config protos are not a wire API.
