# Contributing to Reliant

## Prerequisites

- Go 1.25+ (see `go.mod`)
- Node.js 18+
- Make
- Go dev tools: [air](https://github.com/air-verse/air) (hot reload), [goose](https://github.com/pressly/goose) (migrations), [sqlc](https://sqlc.dev/) (query generation)

## Development Setup

```bash
git clone https://github.com/reliant-labs/reliant.git
cd reliant
npm install
make generate          # generate Go code (sqlc, protobuf, etc.)
npm run dev            # starts Go backend + Vite + Electron with hot reload
```

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