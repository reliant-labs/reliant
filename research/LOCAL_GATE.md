# Local gate: working without GitHub Actions

## Why (settled)

control-plane is the only billed repo (private, free plan). Its Actions budget
blocks most jobs and the owner will NOT raise it. Every run fails with "The job
was not started because an Actions budget is preventing further use". So for
control-plane, CI is not a gate any more. Whatever runs locally IS the gate.
Background: research/CI_SPEND.md (beside this file).

What depends on Actions in control-plane today:
- **PR checks (`ci.yml`, plus ci-extras/dev/e2e/pre-commit/license-gate/pin-drift):**
  the logic is YAML steps, with no single local command that runs the same set.
  **Gap.**
- **Deploys:** already local (`forge env deploy <env>`, docs/runbook.md). Fine.
- **Release cut (`cut-release.yml`, dispatch-only):** blocked. Its core is
  `forge env build <env> --release vX.Y.Z` + `forge env deploy <env> vX.Y.Z`
  + open a PR with the ledger. **Gap.**
- **`build-images.yml`:** runs on tags only; the cut builds the images itself.

## Principle (settled)

**CI is a thin wrapper around a local gate.** One definition of each lane lives
in the project's Taskfile (or a script it calls). The workflow only prepares the
environment (checkout, toolchains, caches, cloud auth) and calls the same task.
A developer or agent runs the identical command locally, so "CI didn't run" stops
meaning "unverified".

## Task names (settled — forge's scaffold and control-plane both use these)

| Command | Meaning | CI job that calls it |
|---|---|---|
| `task ci` | The whole PR gate, locally: `ci:checks`, then `test`, then `test:integration`. Runs everything (no change detection). | none; this is what you run instead of CI |
| `task ci:checks` | Static and build lane: verify-generated, lint, build, vuln-scan, migration safety, KCL validation, plus project extras | `checks` |
| `task test` | Unit tests (existing) | `test` |
| `task test:integration` | Integration tests (existing in forge's scaffold; add it where missing) | `integration-test` |

Semantics `task ci` and `task ci:checks` MUST have:
1. **Report every failure, then exit non-zero.** Run each check even if an
   earlier one failed (one pass shows all failures), print a summary table
   (check, PASS/FAIL/SKIP, duration), and exit 1 if any check failed.
2. **SKIP is not PASS.** A check whose tool or prerequisite is missing (no
   docker, no sibling checkout, no cloud creds) is reported **SKIPPED** with the
   reason, and the gate exits non-zero. A silent skip is false confidence. Allow
   explicit skips with something like `CI_SKIP=check1,check2`; the summary
   still lists them.
3. **Pinned tools, same as CI.** Where CI installs a pinned tool version
   (golangci-lint, govulncheck, buf, kcl, forge), the local gate uses that same
   version (install into a project-local bin, or `go run tool@version`). Example
   failure this prevents: this sandbox's golangci-lint was built with go1.26
   and cannot lint go1.27 code.
4. **The summary must work in CI too.** If `$GITHUB_STEP_SUMMARY` is set,
   append the table to it.

Change detection stays in workflow YAML: it is a CI cost optimisation. Locally
you scope by running one lane.

## Not settled (decide in your task and say why)
- How "continue on failure plus summary" is implemented: a small script the
  Taskfile calls, or a forge-provided helper. Prefer the simplest thing that
  meets semantics 1–4 and that forge can scaffold.
