# Contributing to Reliant

## Prerequisites

- Go (the version in `go.mod`; code generation downloads it if yours differs)
- Node.js 20.19+
- Make, and Python 3 for the generated docs pages
- Go dev tools: [air](https://github.com/air-verse/air) (hot reload), [goose](https://github.com/pressly/goose) (migrations), [sqlc](https://sqlc.dev/) (query generation)

## Development Setup

```bash
git clone https://github.com/reliant-labs/reliant.git
cd reliant
npm install
npm run dev            # starts Go backend + Vite + Electron with hot reload
```

Generated code is committed, so a fresh clone builds as-is — see
[Code generation](#code-generation) for when to regenerate.

Ports are dynamically allocated — check `.dev-ports.sh` for current values.

To create a production build of the desktop app:

```bash
npm run build
```

### Building only the Go CLI

If you only need the `reliant` CLI (no Electron/UI):

```bash
git clone https://github.com/reliant-labs/reliant.git
cd reliant
make generate
make build
```

The binary is written to `dist/reliant`; move it somewhere on your `$PATH`, for example `sudo mv dist/reliant /usr/local/bin/reliant`. The binary targets Reliant's hosted platform by default, so it needs no flags:

```bash
reliant daemon start --token
```

To check that a rebuilt CLI is the one on your `PATH`, run `reliant --version`.

## Code generation

Everything generated is committed: protobuf code (`gen/`, `web/src/gen/`), sqlc
output, and the reference docs (`generated/docs-source/`, `docs/reference/`,
the workflow-builder skill). CI's **Generated code is up to date** job runs
`make generate-all` on every PR and fails on any diff. So after changing an
input — a `.proto`, a query or migration, a tool, a model, a CLI command, the
forge pin — regenerate and commit the result in the same change:

```bash
make generate-all      # everything: protobuf (Go + TS), sqlc, docs
make proto-generate    # protobuf only (Go + TS, including the control-plane client)
make generate          # everything except the reliant TypeScript protobufs
make help              # every individual generate-* target
```

Regenerating an unchanged tree is a no-op on every machine, because nothing a
generator runs comes from the machine:

| Tool | Pinned by |
|------|-----------|
| buf | `BUF_VERSION` in the Makefile, built from source into `~/go/bin/buf-<version>` on first use |
| protoc-gen-go, protoc-gen-connect-go | `tool` directives in `go.mod`, run with `go tool` |
| protoc-gen-es | `tools/protoc-gen-es/package-lock.json`, installed by `make` on first use |
| sqlc | `SQLC_VERSION` in the Makefile |
| Go toolchain | `go.mod`: generation sets `GOTOOLCHAIN` to its version |

Every buf plugin is local, so generation never touches the Buf Schema Registry:
no rate limit (`resource_exhausted: too many requests`), no token, and no buf
install of your own — you need Go, Node, Python 3 and make.

**Always regenerate through `make`, not a bare `buf generate` or `go run`.**
The Makefile sets two things a bare command would take from your machine:

- `GOTOOLCHAIN` — protoc-gen-go formats its output with the `go/printer` of
  the Go that built it, and doc-comment layout changes between Go releases.
  A different local Go would rewrite `*.pb.go` files nobody touched.
- `GOWORK=off` — a local `go.work` (`use ../forge`) would compile the
  generators against your forge checkout instead of `go.mod`'s pin, and
  `cli.md`, which embeds forge's command tree, would document that branch.

To bump a pinned tool, change the pin (`go.mod`, `tools/protoc-gen-es`, or the
Makefile, plus any `buf.gen*.yaml` path that names the version), run
`make generate-all`, and commit the regenerated code with it.
`internal/buildmode/buf_plugin_pins_test.go` rejects remote or unpinned
plugins. `make pin-forge` regenerates for you.

## Running Tests

```bash
make test              # Go tests
npm run verify:dev     # full verification (lint + type-check + tests)
```

## Making Changes

1. Fork the repo and create a branch from `main`.
2. Make your changes.
3. Add or update tests as needed.
4. Ensure `make test` passes.
5. Open a PR against `main`.

## Additional Docs

The [`contributing/`](contributing/) directory has detailed guides for maintainers and contributors:

- [Config Reference](contributing/CONFIG_REFERENCE.md) — configuration options and environment variables
- [Release Setup](contributing/RELEASE_SETUP.md) — how to cut releases, code signing, and distribution
- [WSL Development](contributing/WSL_DEVELOPMENT_SETUP.md) — setting up a dev environment on Windows/WSL

## Code Style

- **Go:** Follow the project's `golangci-lint` configuration. Run `golangci-lint run` before submitting.
- **TypeScript/React:** Follow the project's ESLint configuration. Run `npm run lint` before submitting.

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