# Contributing to forge

Thanks for your interest in contributing! This doc covers the bare minimum to
get a working dev loop and land a PR.

## Prerequisites

- Go (matching the version in `go.mod`)
- Docker (for local Postgres / OTEL collector / other dev infra)
- [`task`](https://taskfile.dev) for the repo's automation entrypoints
- [`buf`](https://buf.build) for proto codegen
- [`pre-commit`](https://pre-commit.com) — see **Pre-commit hooks** below

New contributors can bootstrap every tool on PATH via:

```bash
bash scripts/bootstrap.sh
```

Or open the repo in the supplied devcontainer (`.devcontainer/devcontainer.json`),
which runs the same script automatically.

## Pre-commit hooks

This repo ships a [`.pre-commit-config.yaml`](./.pre-commit-config.yaml) that
formats Go, runs `go vet`, checks proto formatting, scans for leaked
secrets (gitleaks), and formats frontend/docs files. Install it once:

```bash
pip install pre-commit
pre-commit install
```

Afterwards the hooks run on every `git commit`. CI runs the same hooks
(`.github/workflows/ci.yml`'s checks job), so skipping the local install
only means you'll find out about issues a bit later.

## Dev loop

```bash
# 1. Install Go (and frontend, if present) dependencies
task deps

# 2. Bring up local infrastructure (Postgres, etc.) via docker compose
task dev-up

# 3. Run the app with hot-reload
task dev
```

Stop infra with `task dev-down` when you're done.

### Dev-installing the `forge` CLI

To iterate on the CLI itself, install it from your checkout with the local
source root stamped in:

```bash
make dev            # or: task install:dev
export PATH="$HOME/go/bin:$PATH"   # so the freshly-installed forge is found
```

`make dev` runs `go install` with an ldflag that records THIS checkout's path
(`git rev-parse --show-toplevel`) into the binary. The path is discovered at
build time from your own tree — nothing machine-specific is ever committed to
forge's source.

Why it matters: a released `forge project new` pins forge at the binary's own
published version, with no `replace`. A `make dev` binary has NO such version —
its bytes are on no module proxy — so instead of inventing one it writes a
**gitignored `go.work`** into each new project that `use`s `<this-checkout>`,
and scaffolds build against your in-development forge with no manual
`go mod edit -replace`. `task build` / `task install` stamp the same root: any
binary built from a checkout is on no proxy, so bridging is the only way its
scaffolds can resolve forge. A released `forge` (`go install …@vX.Y.Z`) omits
the stamp and never writes that `go.work`.

The same rule applies to an EXISTING project: running a `make dev` forge's
`forge generate` against a project pinned to a published forge is refused
before codegen touches the tree, with the `go work use` command to run. Pinning
the last release instead is how a generated tree came to call
`testkit.StubNotConfigured` against a forge that did not have it.

## Before opening a PR

Keep the feedback loop tight — run these locally and make sure they're green:

```bash
task lint   # golangci-lint + buf lint (+ frontend lint if configured)
task test   # the module under -race in CI's three lanes: unit, internal/cli, guards (excludes e2e by build tag)
```

For changes to scaffolding, templates or the generate pipeline, also run the
e2e suite, or the part of it your change reaches:

```bash
task test:e2e                                # every TestE2E* test (-tags e2e); tens of minutes
task test:e2e -- -run TestE2EFixtureCorpus   # a slice of it
```

CI runs it sharded in `.github/workflows/e2e-suite.yml` on every push to main and nightly,
and on a pull request labelled `ci:e2e`; `reliant.md` ("CI: what runs where") says what runs
in which workflow.

## Commit messages

This project follows [Conventional Commits](https://www.conventionalcommits.org).

```
<type>(<scope>): <summary>

<optional body>
<optional footer>
```

Common types: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `ci`, `build`.
The scope is the package or area you touched (e.g. `api`, `db`, `deploy`).

The pre-commit `commit-msg` hook enforces this via
[commitlint](https://commitlint.js.org/). Install hooks with
`pre-commit install --hook-type commit-msg` to catch violations locally.

## Pull requests

- Branch from `main`, rebase before you open the PR.
- Keep PRs focused — one logical change per PR.
- Fill in the PR description: what changed, why, and how you verified it.
- CI must be green before merge.

## License of contributions

Forge is licensed under the [Apache License, Version 2.0](LICENSE). Under its
section 5, anything you submit for inclusion is contributed under those same
terms. There is no contributor license agreement to sign.

Instead, certify each commit under the
[Developer Certificate of Origin](https://developercertificate.org/) by
signing it off:

```bash
git commit -s
```

That appends `Signed-off-by: Your Name <you@example.com>` and states that you
wrote the change, or otherwise have the right to submit it under the
project's license.

## Filing issues

Please open issues in this repository's issue tracker with:

1. A clear, reproducible description (version, OS, command, expected vs actual)
2. Relevant logs or stack traces (with secrets scrubbed)
3. The smallest repro you can manage

## Project conventions

Run `forge skill list` to discover Forge-specific conventions — proto-first
design, `gen/` ownership, and the CLI commands. See `reliant.md` at the
project root for the critical rules.
