---
name: ci-toolchain
description: How CI resolves the project's Forge pin and installs codegen tools and frontend dependencies before verifying generated files.
---

# CI toolchain

### Which forge CI installs

Every job that runs forge installs it from the PROJECT, at run time — no
workflow carries a version literal. All of them run one shared script
(`installForgeScript` in forge), whose core is:

```sh
mod=$(GOWORK=off go mod edit -json)          # reads the go.mod FILE: no network, no cache
if <go.mod requires github.com/reliant-labs/forge>; then
  v=$(GOWORK=off go list -m -f '{{.Version}}' github.com/reliant-labs/forge)
else
  v=<forge_version from forge.yaml>
fi
go install "github.com/reliant-labs/forge/cmd/forge@${v}"
```

go.mod's forge requirement is the forge the code compiles against and the
one that generated it, so **bumping forge in go.mod is the whole upgrade** — the
workflows follow on their own. A version stamped into a workflow at scaffold
time froze there while go.mod moved on: that is how a project ended up
verifying its generated code with an older forge than the one that wrote it
("refusing to overwrite .forge-kcl/ with an OLDER forge's KCL module").

- WHETHER go.mod requires forge is read from the file, never inferred from a
  lookup failing. Only a module that does not require forge at all (a
  `--kind cli` / `library` project) uses forge.yaml's `forge_version`. (For a
  module that does, `forge generate` keeps `forge_version` equal to go.mod's
  require, so the two never disagree in a generated tree.)
- If go.mod requires forge and the version cannot be resolved, the step FAILS
  with go's own error. It never falls back to forge.yaml — installing a
  different forge than the code compiles against is the drift this exists to
  prevent.
- A `replace` of forge in go.mod fails the step with `::error` naming it — CI
  cannot install a local checkout. Bridge one with an uncommitted `go.work`.
- A pin no module proxy can serve (`+dirty`, `dev`, `0.0.0`) fails the same way.
- It needs `jq` (preinstalled on GitHub-hosted runners). No C toolchain is
  needed.

The `checks` job also installs, before `task ci:checks` runs verify-generated, the codegen toolchain at go.mod's
versions — `forge tools install --force` (protoc-gen-go, protoc-gen-connect-go,
goimports) — and runs `npm ci` in each frontend, because it regenerates the
tree and demands identical bytes. `npm ci` is what installs
`@bufbuild/protoc-gen-es`, from the frontend's own devDependencies and
lockfile: `forge tools install` never runs npm and never writes a
frontend's `package.json` or `package-lock.json` (`--force` reinstalls the
Go tools only). It fails, naming the edit, when a frontend's buf.gen.yaml
runs the plugin but its package.json does not declare it. `forge ci
verify-generated` refuses to run when a frontend's protoc-gen-es is missing
rather than certify a tree whose TypeScript stubs it skipped, and refuses
to regenerate over a tree an earlier step already modified — naming those
paths as changed before `forge generate` ran, not as generated-code drift.
