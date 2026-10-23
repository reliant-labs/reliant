---
name: config-files
description: Loading a binary's typed config from a YAML/JSON file — --config and the per-binary <BINARY>_CONFIG path variable, precedence, the override, what a missing or invalid file does, and how a root and a command config share one flat file.
---

# Config files

Every config also loads from a file: `--config <path>` (`.yaml`, `.yml` or `.json`, keyed by the proto field names), or — when the flag is absent — the binary's OWN path variable, `<BINARY>_CONFIG`: `RELIANT_CONFIG` for `cmd/reliant`, `IDP_PROVISION_ONESHOT_CONFIG` for `cmd/idp-provision-oneshot`. A binary's root config and its command configs share it. The project-global `AppConfig`, bound to no binary, reads `<PROJECT>_CONFIG` (`config.ConfigPathEnv` in the generated `pkg/config`). Precedence is defaults < file < env < flags.

- The variable is per binary on purpose: one shared name would let a shell that sets it for one app make every other forge binary launched from that shell load a file written for a different schema and fail at startup.
- A path that is given but missing, unreadable or invalid fails startup, naming where it came from (`config file "/x.yaml" (from env RELIANT_CONFIG) does not exist`). No path given is not an error: the file layer is skipped. A variable set to `""` is unset, like every variable the loader reads.
- A key the file names sets that field outright, and every key it omits keeps its default. A list or map REPLACES the default, as an env var or flag does (`allowed_registries: []` clears a defaulted list); a zero value (`port: 0`, `enabled: false`) overrides it; a nested block takes only the leaves it names; `null` sets nothing. Durations are protojson strings in seconds (`timeout: 0.5s`), not Go durations.
- One file configures one PROCESS, and it is flat, like the process's flags and env vars: `app server api` reads its root config AND `ApiServerConfig` from the same file, each taking its own top-level keys.

  ```yaml
  # app server api --config api.yaml
  log: {level: debug}                 # AppConfig (root)
  grpc_port: 9090                     # ApiServerConfig (server api)
  ```

  A key that no config the command loads declares is an error naming them all (`unknown key "grcp_port": no config this command loads has such a field (ApiServerConfig, AppConfig)`), so a misspelling still fails — and so does a key only another process reads: `app server worker` refuses `grpc_port`. Give each process its own file. The configs a command loads are the ones registered on its path (`config.Register<Msg>Flags`).
- To answer to a different variable, call `forgeconfig.RegisterConfigFlag(root, "MY_APP_CONFIG")` (`forgeconfig` = `github.com/reliant-labs/forge/pkg/config`) on the binary's root command BEFORE `config.Register<Msg>Flags(root)`: the first `--config` registered on a command path names the variable for every load beneath it.

## Build-time defaults

Between the proto `default_value` and the file sits one more layer: values a BUILD carries. A release binary stamped with its environment's endpoints, while `go install` of the same source keeps the proto default:

```bash
go build -ldflags "-X 'example.com/app/pkg/config.BuildDefaults=APP_SERVER_URL=https://api.example.com&APP_REGION=eu'" ./cmd/app
```

`BuildDefaults` is one string in URL query form, keyed by each field's `env_var` (escape `&`, `=`, `+`, `%` or a space in a value with `url.QueryEscape`). Precedence: `default_value` < build default < file < env < flags. A key naming no config field of the binary, a `sensitive` field, a duplicate key, or a value that does not parse fails every `Load` naming the key — a stamp is a build defect, never silently ignored.
