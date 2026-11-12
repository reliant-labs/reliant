---
name: deploy/kcl-imports
description: Import ANOTHER forge project's KCL (its workloads.k, config_gen.k, a shared infra repo's envs) — `[forge_projects]` in deploy/kcl/kcl.mod, either at the version go.mod already pins (replace dir, go.work use, module cache + go.sum) or by a git pin (repo + ref + commit lock, no go.mod), the forge-pin skew refusal, and `forge kcl imports`.
---

# Importing another forge project's KCL

A project that deploys another project's binaries should deploy them from that
project's OWN declarations — its `fw.Workload`s and its typed config — not from
a second copy kept in step by hand. "The other project declares, this one
binds."

```toml
# deploy/kcl/kcl.mod
[package]
name = "control-plane-deploy"
edition = "v0.11.4"
version = "0.1.0"

[dependencies]

[forge_projects]
reliant = { module = "github.com/reliant-labs/reliant" }   # subdir defaults to "deploy/kcl"
```

```python
import reliant.workloads as rwl
import reliant.config_gen as rcfg

api = rwl.api_server | {runtime = forge.OnCluster {target = _prod}}
```

## A `module =` import has no version field: go.mod is the pin

The KCL is read from exactly where the go build reads the module, by the go
command's own rules:

| go.mod / go.work says | forge reads |
|---|---|
| `go.work` `use ./reliant` | that directory |
| `replace github.com/x/reliant => ../reliant` | the sibling checkout, as it is (dev) |
| `replace github.com/x/reliant v1.2.3 => github.com/fork/reliant v1.2.4` | the module cache at the replacement |
| `require github.com/x/reliant v1.2.3` | the module cache at `v1.2.3`, verified against go.sum |

A module not yet in the cache is fetched with `go mod download` (your
GOPROXY / GOPRIVATE / credentials), in a scratch directory — a render never
edits go.mod or go.sum. The zip hash must match go.sum, as the go build
requires. So in CI, the step that already downloads modules for the build is
all an import needs; offline, put the module in the cache first.

To move the version, move the Go pin: `go get github.com/x/reliant@<commit>`.
`version`, `ref`, `commit`, `tag` keys beside `module` are refused — a second
pin is the drift this exists to remove.

**Not a Go dependency?** If your Go code does not import the other module, or
there is no Go at all, there is no Go pin to share: pin the repository by git
in the same table.

```toml
[forge_projects]
infra = { repo = "github.com/acme/infra", ref = "v1.4.0", commit = "8f2c1e0b9d4a", subdir = "deploy/kcl" }
```

- `repo` and `ref` are a `forge.GitSource`'s: fetched once into the shared
  source cache, keyed by repo + ref, and overridable to a working copy in the
  gitignored `.forge/source-overrides.yaml` (reported as `NOT the pinned ref`;
  a release refuses to cut while one is in use).
- `commit` (optional, 7+ hex) is the lock: a ref that resolves elsewhere is a
  `pin mismatch`, naming both commits. A full-sha `ref` is its own lock.
  `forge kcl imports` notes each unlocked pin with the commit to lock.
- Offline: a cached pin renders with no network. `forge kcl imports` fetches
  every pin (prepare a machine or a CI cache with it); `--refresh` re-fetches.
- The imported repo's `forge.yaml` may be anywhere from `subdir` up to the
  repository root. No go.mod is needed on either side.
- `module` and `repo` are mutually exclusive per entry.

Polyrepo topologies, costs and the deploy-repo shape: `deploy/polyrepo`.

## Refusals (each names the fix)

- **Pin mismatch** (git pin): the ref resolved to another commit than
  `commit`. `forge kcl imports --refresh`, or update `commit` on purpose.
- **Unresolvable pin**: offline on a cold cache, a wrong ref, or git cannot
  authenticate; the error names the pin and the override-file escape.

- **Forge version skew.** The imported KCL's `import forge` resolves to YOUR
  render's forge module (there is one per evaluation). When the imported
  project's forge.yaml `forge_version` differs from yours, the render refuses
  and prints both pins. Make them agree — bump the other project's pin to a
  version that pins your forge, or bump yours. `FORGE_ALLOW_VERSION_SKEW=1`
  renders anyway.
- **go.mod does not require the module** (the error offers the git pin), or
  no go.mod for a `module =` import, a missing replace directory, a
  directory whose go.mod declares another module, a missing go.sum hash, a
  cache entry that does not match go.sum.
- **Not a forge project** (no forge.yaml at the module root), or no kcl.mod at
  `subdir`.
- **Name shadowing**: `forge`, `kcl_plugin`, a KCL system module (`yaml`,
  `json`, …), a `<name>.k` / `<name>/` at your KCL root, or a kpm dependency of
  the same name.
- **`forge_project = …` under `[dependencies]`.** kpm owns that table and cannot
  resolve it; the error shows the `[forge_projects]` spelling.
- **kpm dependencies inside the imported package** — forge evaluates it in
  place and resolves only its own `[forge_projects]`.

The imported package's own absolute imports (`import config_gen`) resolve
against ITS root, so both projects keeping a `config_gen.k` is fine. Its
`[forge_projects]` are resolved too, against YOUR go.mod.

## Seeing what was resolved

```bash
forge kcl imports           # NAME MODULE|REPO VERSION VIA DIR (fetches git pins)
forge kcl imports --refresh # re-fetch git pins (a moved branch or tag)
forge kcl imports --json    # for scripts and tests
```

A Go test that renders through a forge CHILD process does not see the child's
file reads in Go's test cache. If such a test runs under `-short`, it must stat
the imported files itself: walk each `dir` from `forge kcl imports --json`
(or mark the test slow).
