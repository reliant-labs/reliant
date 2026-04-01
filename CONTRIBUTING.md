# Contributing to Reliant

Thanks for your interest in contributing to Reliant.

## Prerequisites

- Go 1.25+
- Node.js 18+
- Make

## Development Setup

```bash
git clone https://github.com/reliant-labs/reliant.git
cd reliant
npm run dev
```

This starts the Go backend, Vite dev server, and Electron app with hot reloading.

## Running Tests

```bash
make test
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
- [Backend Integration](contributing/BACKEND_INTEGRATION.md) — backend architecture and integration details
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