# Contributing to Reliant

## Prerequisites

- Go (the version in `go.mod`; code generation downloads it if yours differs)
- Node.js 20.19+
- [Task](https://taskfile.dev) 3.38+, and Python 3 for the generated docs pages
- [air](https://github.com/air-verse/air) (hot reload of the server roles in the dev stack)

- [forge](https://github.com/reliant-labs/forge), for `forge env up dev`:
  `go install github.com/reliant-labs/forge/cmd/forge@latest`. A forge that is
  not the version `forge.yaml` pins hands most commands (`env up`, `env config`)
  to that version, building it once; where it cannot, it says so and prints the
  `go run github.com/reliant-labs/forge/cmd/forge@<pin> …` to run instead.

Every task runs the forge `forge.yaml` pins (`go run
github.com/reliant-labs/forge/cmd/forge@<pin>`), whatever `forge` is on your
`PATH`. `task --list` names every task.

## Development Setup

```bash
git clone https://github.com/reliant-labs/reliant.git
cd reliant
task deps              # Go modules + the web, electron and protoc-gen-es npm packages
forge env up dev       # the whole product on this machine (deploy/kcl/dev)
```

`forge env up dev` runs the three server roles (`reliant server
api|worker|gateway`) under air, a `reliant db migrate up` job, and the
`frontends/web` Vite dev server, all as host processes, over forge's host
Postgres 16, NATS and Temporal dev server. It needs no docker, no k3d and no
control plane. Sign in to the web app with the API key forge generated:

```bash
grep AUTH_API_KEY secrets/dev.yaml
```

`deploy/kcl/dev/main.k`'s header shows how to connect a daemon and give the
account a model. Every worktree gets its own stack with its own 100-port block
and its own data, so the ports are never fixed. Ask forge, not a file:

```bash
./.reliant/tools/port-info.sh   # each role's port, and whether it is listening
forge env config dev            # every variable each process was started with
forge env down dev              # stops this checkout's stack only
```

The logs are in `.forge/logs/dev/` (one file per workload, browser console
included); the `dev-logs` skill (`.reliant/skills/dev-logs/SKILL.md`) is the
map. The full platform (billing, the LLM gateway, managed workspaces, the
desktop app) is control-plane's `forge env up dev`, in a `../control-plane`
checkout; it needs k3d.

To create a production build of the desktop app:

```bash
cd electron && npm run build
```

Generated code is committed, so a fresh clone builds as-is — see
[Code generation](#code-generation) for when to regenerate.

### Building only the Go CLI

If you only need the `reliant` CLI (no Electron/UI):

```bash
git clone https://github.com/reliant-labs/reliant.git
cd reliant
task build
```

The binary is written to `dist/reliant` with the release identity stamped in;
move it somewhere on your `$PATH`, for example `sudo mv dist/reliant
/usr/local/bin/reliant`, or `task install` it. The binary targets Reliant's
hosted platform by default, so it needs no flags:

```bash
reliant daemon start --token
```

To check that a rebuilt CLI is the one on your `PATH`, run `reliant --version`.

## Code generation

Everything generated is committed: protobuf code (`gen/`, `frontends/web/src/gen/`), the
forge ORM projection (`internal/store/*_gen.go`), and the reference docs (`generated/docs-source/`, `docs/reference/`,
the workflow-builder skill). The gates fail on any diff a regenerate makes (the
merge gate when your change reaches a generator's input, the release gate on
every release). So after changing an input — a `.proto`, a migration, a tool,
a model, a CLI command, the forge pin — regenerate and commit the result in the
same change:

```bash
task generate              # everything: the pinned forge generate, then every docs generator
task generate:forge        # forge generate alone: protobuf (Go + TS), ORM, mocks, and forge.yaml's generate.hooks
task generate:docs         # the docs generators alone
task generate:controlplane # the control-plane client alone, from proto-vendor/
```

`forge generate` runs buf over `buf.gen.yaml`, projects the ORM, mocks,
`gen/forge_descriptor.json`, the per-service test harnesses and
`deploy/observability/`, then the compiled-code generators forge does not own,
declared as `generate.hooks` in `forge.yaml` (the control-plane client, the
YAML bindings, the tool catalog, the integration manifest schema and event
projections). Two checks judge what each half would change, and fail naming
every file:

```bash
task generate:check        # the pinned forge generate is a no-op
task generate:docs:check   # every docs generator is a no-op
```

`task test:affected` and the merge gate run them for you when your diff
touches one of their inputs.

Regenerating an unchanged tree is a no-op on every machine, because nothing a
generator runs comes from the machine:

| Tool | Pinned by |
|------|-----------|
| forge | `forge_version` in `forge.yaml` (and the matching `go.mod` require) |
| buf | the `github.com/bufbuild/buf` line in `go.mod`, built in its own module graph |
| protoc-gen-go, protoc-gen-connect-go | `tool` directives in `go.mod`, run with `go tool` |
| protoc-gen-es | `tools/protoc-gen-es/package-lock.json`, installed by `task deps:protoc-gen-es` on first use |
| Go toolchain | `go.mod`: generation sets `GOTOOLCHAIN` to its version |

Every buf plugin is local, so generation never touches the Buf Schema Registry:
no rate limit (`resource_exhausted: too many requests`), no token, and no buf
install of your own — you need Go, Node, Python 3 and Task.

**Always regenerate through `task`, not a bare `buf generate` or `go run`.**
The Taskfile sets two things a bare command would take from your machine:

- `GOTOOLCHAIN` — protoc-gen-go formats its output with the `go/printer` of
  the Go that built it, and doc-comment layout changes between Go releases.
  A different local Go would rewrite `*.pb.go` files nobody touched.
- `GOWORK=off` — a local `go.work` (`use ../forge`) would compile the
  generators against your forge checkout instead of `go.mod`'s pin, and
  `cli.md`, which embeds forge's command tree, would document that branch.

To bump a pinned tool, change the pin (`go.mod`, `tools/protoc-gen-es`, plus
any `buf.gen*.yaml` path that names the version), run `task generate`, and
commit the regenerated code with it. `internal/buildmode/buf_plugin_pins_test.go`
rejects remote or unpinned plugins. `task pin:forge` bumps forge and regenerates
for you ([dev-docs/pinning.md](dev-docs/pinning.md)).

## Running Tests

```bash
task test:affected                     # FIRST: only the tests your diff can reach, and why
task test:short -- ./internal/<pkg>/...  # the inner loop: -short, cached, no -race
task test                              # everything: Go -race -count=1, web, electron
task test:integration                  # integration-tagged Go tests
task test:e2e                          # the e2e/stories (they start their own Temporal dev server)
```

`task test:affected` diffs against the merge-base with `origin/main`, runs the
Go packages whose test binaries contain a changed package, the vitest files that
import a changed module, and the electron tests when electron or a file they read
changed, and prints why each ran. A `go.mod`, `package.json`, `Taskfile.yml` or
config change, or any path nothing claims, runs the whole lane.
`task test:affected:plan` prints the decision without running it. The rule
table and its blind spots: [dev-docs/testing-affected.md](dev-docs/testing-affected.md).

A test that takes more than about two seconds under `-short` calls
`testkit.Slow(t, "<what makes it slow>")` first: it then skips under `-short`
and runs in `task test` and the release gate. Gate a slow test; never weaken
its assertion.

### Which database the Go tests use

None you have to set up. Every DB-backed test gets a database of its own from
forge pgtest: an embedded Postgres of the major `forge.yaml` declares
(`database.postgres_version`), booted on a free port on first use and shared by
every test process of your user on the machine, with each test working in its
own copy of a migrated template. No docker, no fixed port, and it runs under
`-short` too.

- **`FORGE_TEST_POSTGRES_URL`** (a maintenance DSN, database `postgres`, on the
  declared major) points a run at a server of your own instead.
- **`DATABASE_URL` is never read by a test.** It is the server's process
  configuration, and a shell that exported a dev stack's would otherwise point
  `go test` at real data.

## Making Changes

1. Fork the repo and create a branch from `main`.
2. Make your changes.
3. Add or update tests as needed.
4. Run the merge gate: `task ci:merge`. It is what every merge must pass (below).
5. Open a PR against `main`.

### How a change merges

reliant has two local gates ([dev-docs/merge-and-release-gates.md](dev-docs/merge-and-release-gates.md)):

| Gate | Command | Judges | When |
|---|---|---|---|
| merge | `task ci:merge` (`scripts/merge-gate.sh`) | only what your diff can affect: the affected tests (`-short`, direct importers), `go build ./...`, `forge lint --scope <changed dirs>`, and the generate/docs no-op checks when their inputs changed; about five minutes | before every merge |
| release | `task release:gate` (`scripts/release-gate.sh`) | everything, on a commit of main: unscoped lint, `task test` (`-race`, every slow test), integration, e2e, generate and docs drift; recorded per sha | before every release: `task release:tag` refuses a commit without a green record |

`main` accepts a merge only when its commit carries a successful `merge-gate`
commit status (the "main: merge-gate required" ruleset; no bypass, `gh pr merge
--admin` included). The maintainers' merge queue posts it: it merges `main`
into the branch (never a rebase), squashes, runs `scripts/merge-gate.sh` on
that exact commit and lands it. The only other way in is an audited waiver,
for a hotfix that cannot wait or a gate failure that reproduces identically on
`main`, which posts `WAIVED: <reason>` on the commit and comments on the PR.

CI (`.github/workflows/ci.yml`) runs the same two gates as thin wrappers:
`task ci:merge` on every PR, `task release:gate` on every push to main and
nightly, plus the lanes neither covers (the renderer's release build,
Playwright, the electron package, the vendored control-plane contract). It is
a second signal on the same commands, never a different opinion.

## Additional Docs

The [`contributing/`](contributing/) directory has detailed guides for maintainers and contributors:

- [Config Reference](contributing/CONFIG_REFERENCE.md) — configuration options and environment variables
- [Release Setup](contributing/RELEASE_SETUP.md) — how to cut releases, code signing, and distribution
- [WSL Development](contributing/WSL_DEVELOPMENT_SETUP.md) — setting up a dev environment on Windows/WSL

## Code Style

- **Go:** Follow the project's `golangci-lint` configuration. `task lint` runs the pinned `forge lint` (golangci-lint, buf lint and forge's convention lanes).
- **TypeScript/React:** Follow the project's ESLint configuration. Run `task lint:web` (and `task lint:electron`) before submitting.

## Commit Messages

Use [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add workflow retry logic
fix: resolve race condition in agent pool
docs: update API reference
```

## License

By contributing, you agree that your contributions will be licensed under the [Business Source License 1.1](LICENSE). After 4 years, each version converts to Apache 2.0.

## Code of Conduct

This project follows the [Contributor Covenant Code of Conduct](CODE_OF_CONDUCT.md). Please read it before participating.