# Consumer canary: build reliant and control-plane against your forge

forge's contract with its consumers (decision D2, `research/pinning/OPTIONS.md`):
**forge may break freely, but a breaking forge PR ships its codemod and keeps the
reliant and control-plane builds green.** The canary is the second half; the
first is the [API gate](api-breaks.md).

## What it does

For each consumer, at its `main`:

1. exports its tree (`git archive`) into a scratch directory — the consumer's
   checkout is never read or modified, and nothing is ever committed to it;
2. points it at your forge checkout with a throwaway `-modfile` that carries
   `replace github.com/reliant-labs/forge => <your checkout>` (the consumer's own
   `go.mod`/`go.sum` are untouched);
3. runs `go build ./...`, `go vet ./...`, and `go test -short` of **only the
   packages that import a forge package the diff changed** (found with
   `go list -test -json`, so a package that imports forge only from a test file
   counts).

A step that fails is re-run against forge **at the revision the diff is judged
against** (the PR's base). A failure that occurs there too predates your change
(the consumer is red on its own, or forge's main already broke it): the run
reports it ("PASS (failures predate this change)") and does not fail. Only what
your change adds fails the run. A `-force` run (nightly) has no diff and no
baseline, so every failure counts: that is how a consumer that is already red
against forge main is noticed. Full output of every failed step, for both
forges, is kept under `<cache>/logs/`.

## When it runs

| trigger                         | runs                                                                                                                                                                         |
| ------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `task canary`                   | locally, on demand                                                                                                                                                           |
| push to `main`                  | `consumer-canary.yml`, always                                                                                                                                                |
| nightly                         | `nightly.yml` → `consumer-canary.yml` with `-force` (consumers moved under an unchanged forge; no baseline, every failure counts); a red opens the "Nightly CI is red" issue |
| pull request, label `ci:canary` | `consumer-canary.yml`                                                                                                                                                        |

**Why not on every pull request:** a cold `go build ./...` + `go vet ./...` of
reliant alone measured 215s + 120s on a loaded 32-core box (the build cache was
cold; a warm cache is much faster, which is what the main-push cache gives
CI), and control-plane is a second whole-module build on top. The pull-request tier has a five-minute
median budget and every job added is another runner to queue for (`ci.yml`, "TWO
TIERS"), so the canary is a main-push job plus a label-triggered pull-request
job. Label a PR `ci:canary` when it changes `pkg/` or `go.mod`. The cheap half —
the [API gate](api-breaks.md) — does run on every pull request.

The run is skipped (in seconds) when the diff cannot reach a consumer's build:
no non-test Go file under `pkg/` or in a package `pkg/` imports, and no
`go.mod`/`go.sum` change. A `go.mod` edit that leaves every require, replace
and `go` line as it was (promoting a module from indirect to direct) reaches
nothing. A pass is cached by a hash of forge's non-test Go sources and the
consumer's commit, so re-running an unchanged forge is free.

## Running it locally

```bash
task canary                                  # reliant + control-plane vs origin/main
task canary -- -plan                         # say what would run, build nothing
task canary -- -consumer reliant=../reliant  # a local checkout (read via git archive)
task canary -- -consumer reliant=../reliant@my-branch
task canary -- -force                        # even if the diff cannot reach a consumer
task canary -- -no-test                      # build and vet only
```

A consumer URL is fetched shallow (`--depth 1`) into
`$XDG_CACHE_HOME/forge-consumer-canary` using **your** git credentials.
control-plane is private: without an ssh key or credential helper that can read
it, name just reliant: `-consumer reliant=https://github.com/reliant-labs/reliant.git`.
The run holds one host heavy slot (`forge slot status`), like `forge lint`.

## Fixing a red canary

The output names the consumer, the step and the compiler error. Then either:

- fix forge so the consumer still builds (keep the old name as a thin wrapper,
  change the default instead of the signature), or
- ship the migration: a `forge fix <name>` codemod and/or a playbook entry,
  declare the break (`api-break` label / `API-BREAK:` line), and send the
  consumer patch (reliant, then control-plane) that the codemod produces. The
  canary builds consumers at **main**, so it stays red until that consumer
  patch merges _or_ the consumer's pin is what absorbs the change; coordinate in
  the PR and, if a consumer must move first, land its forge-compatible change
  before yours.

## What it does not cover

- It does not run `forge generate` in the consumer, so a template or generator
  change that only breaks _regenerated_ code is not caught. The diff is
  reported ("it changes generated output") and the E2E suite
  (`e2e-suite.yml`: every push to main, or a pull request labelled `ci:e2e`) is
  the check for it.
- It builds the consumers' Go. Frontends, KCL and their gates are the consumers'
  own (`task ci`).
- It tests consumer packages with `-short`, the pull-request tier, not the
  integration lane.

## CI credentials for the private consumer

`consumer-canary.yml` checks control-plane out with the secret
**`CONTROL_PLANE_DEPLOY_KEY`**, a read-only deploy key. It exists in
`reliant-labs/reliant`'s secrets (created 2026-10-11 for reliant's
`cp-contract` job; the deploy key on control-plane is "reliant CI: cp-contract
(read-only)"). It does **not** exist in `reliant-labs/forge`'s secrets.

Until it does, the workflow checks reliant only, and says in the step summary and
as a warning annotation that **control-plane was not checked** — it is never
reported as a pass. To enable control-plane:

1. Generate a new ed25519 keypair (do not reuse the reliant key's private half
   across repositories — a deploy key can be attached to only one repository's
   _keys_ list, but its private half is a secret you would be copying around):
   `ssh-keygen -t ed25519 -N '' -C 'forge CI: consumer-canary (read-only)' -f cp-forge-canary`
2. Add `cp-forge-canary.pub` as a **read-only** deploy key on
   `reliant-labs/control-plane` (Settings → Deploy keys; GitHub does not allow
   the same public key on two repositories, so this must be a new key).
3. Add the private half as the Actions secret `CONTROL_PLANE_DEPLOY_KEY` on
   `reliant-labs/forge`.

Forge is a public repository, so a pull request from a fork gets no secrets and
the canary checks reliant only. A same-repository pull request gets the key.
The key is passed only to the `actions/checkout` step (`persist-credentials:
false`), never to the steps that build code under review.
