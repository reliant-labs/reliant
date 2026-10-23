---
name: subtrees
description: Subtree configs — one typed config several commands of a binary load (every command at or below a path), without every process loading it; registration, the loader's path check, the collision rules and the KCL projection.
---

# Subtree configs

A setting many commands read but the server roles must not carry — the CLI's server URL and token, a guard policy `daemon start` and `open` both run — is a SUBTREE config: loaded by every command at or below each path, and by no other process.

```protobuf
message CLIConfig {
  option (forge.v1.binary_config) = {
    binary: "app"
    subtrees: [{path: ["auth"]}, {path: ["daemon"]}, {path: ["open"]}]
  };
  string server_url = 1 [(forge.v1.config) = {env_var: "APP_SERVER_URL", flag: "server"}];
  string token = 2 [(forge.v1.config) = {env_var: "APP_TOKEN", sensitive: true}];
}
```

```go
config.RegisterCLIConfigFlags(authCmd)     // persistent on EACH subtree's command
config.RegisterCLIConfigFlags(daemonCmd)
config.RegisterCLIConfigFlags(openCmd)
// in any RunE at or below them:
cli, err := config.LoadCLIConfig(cmd)      // refuses a command under none of them
```

- `subtrees` and `command` are exclusive; no path may lie below another of the same message.
- Any two configs one process can load together — root and anything, a subtree config and a command or subtree config at or below it — may not declare the same env var, flag or top-level field; `generate` refuses the pair, naming a process that loads both. Siblings no process loads together may compose the same block: one definition, two values.
- KCL: `<bin>ProcessEnvMap` adds every subtree config whose path prefixes the workload's argv path, so `app server api`'s Deployment carries none of `CLIConfig`. Add the instance to `<bin>_process` in each env's config.k.
- Composition graph: each subtree and command config of the primary binary is a graph input (`g.<Message>`). A root's process loads the ones over its command path, and its reach is the fields its closure reads of them (`architecture`, "Roots and commands"). `roots_gen.go` registers a subtree config's flags on its path's command.

## Roots: each command's settings, and its migrations

Each root's command loads its own configs beside the root config, and
`rootSpec.Configs` (a generated `rootConfigs`, `nil` per config its command
does not load) hands them to `runRoot` before the policy runs. The scaffolded
`serverkitConfig(cfg, root.Configs)` reads the listen port, drain budget,
logging, TLS and database settings from whichever loaded config declares
them — a command config over a subtree config over the root config — so a
role whose port and drain live in its own command config listens and drains
on them. A setting under another name (a `health_port`, a `drain` block) is
yours to read in the same function: serve.go is owned.

What `runRoot` reads is every root's reach, the way what compose.go reads is
one root's: `generate` reads serve.go itself — `cfg` from `config.LoadReach`,
`root.Configs.<Msg>`, and each value it hands a function of the command
tree — so an `observability` block or a `PROMETHEUS_ENABLED` of your own is
in `ROOTS`, `Roots()` and `LoadReach` with nothing declared. A database field
counts only for a root that opens the database. A config handed whole to
another package is the whole of it: pass the fields instead.

A group command (`server` above `server api`) takes its help from a comment
of its own in `internal/app`, by the root doc's rule — first paragraph the
Short, the rest the Long — ending `//forge:group server` (a path:
`//forge:group server,jobs`). Undeclared, it is "The server roots".

`//forge:root worker command=server,worker migrate=wait` leaves the schema to
another process: `apply` migrates before building the root (the default for a
root that reaches the database), `wait` blocks until the schema is current
(`/readyz` names `schema`; `migratekit.AwaitSchema`), and `none` leaves it
alone. `runRoot` reads `root.Migrate` / `root.AwaitMigrations`.

## Each root's env schema, importable

The generated `roots_gen.go` in a graph project's `pkg/config` is `ROOTS` as Go: `config.Roots()`, `config.RootNamed(name)` and `config.ValidateRootEnv(name, env)`. Each `forgeconfig.Root` carries its args, every env var it reaches (config, path, type, required, sensitive), its env families and the configs its process loads. `Root.Validate(env)` runs the boot load over a rendered env (no flags, every problem rather than the first) and adds a `*forgeconfig.UnreachedVarError` for each var the root never reads. The repo that keeps a deployment's values imports this package at its pinned version and checks each workload's env against the root it runs.
