---
name: ci
description: GitHub Actions workflows forge generates — what ships out of the box, the Tier-1 vs Tier-2 boundary, how to add custom workflows alongside the generated ones without losing them on regeneration, and how `forge ci` helper subcommands fit in.
---

# CI

Forge generates a standard set of GitHub Actions workflows under
`.github/workflows/` plus a `.github/dependabot.yml` and (when the
module path is on github.com) a `.github/CODEOWNERS`. The generated
files are Tier-1 codegen — regenerated on every `forge generate` and
banner-protected — but you can add custom workflows alongside them
without forge ever touching your additions.

## What ships out of the box

- `.github/workflows/ci.yml` (yours (scaffold-once)): A thin wrapper around the local gate: jobs `changes`, `checks` (`task ci:checks`), `test`, `integration-test`, plus `docker-build` / `e2e` where the project has them; `timeout-minutes` on every job. See `ci/gate`
- `.github/workflows/proto-breaking.yml` (Tier-1): `buf breaking` (and nothing else — no lint, format, push or PR comment) against the PR's base branch on PRs that touch `proto/**`, `buf.yaml`, or `buf.gen.yaml`. Passes with a notice when the base has no protos yet (the PR introducing the first ones). See the `proto-breaking` skill for the full deprecation flow.
- `.github/workflows/build-images.yml` (Tier-1): The project image: build + push, cosign signature, SBOM, and SLSA provenance (skipped on private repositories, which GitHub's attestation store refuses outside Enterprise Cloud). Frontend images are per-env and built by `deploy.yml`
- `.github/workflows/deploy.yml` (Tier-1): Per-environment `forge env build <env> --push` + `forge env deploy <env> --yes`, one matrix entry per declared `deploy/kcl/<env>/main.k` (dev excluded)
- `.github/workflows/e2e.yml` (Tier-1): E2E suite, label-gated on PRs (`run-e2e`) — emitted when there is a suite to run: the generated `e2e/` harness exists, or `ci.e2e.enabled: true`. docker-compose or k3d runtime
- `.github/workflows/pre-commit.yml` (Tier-2): Runs the `.pre-commit-config.yaml` hook set so contributors who skipped the local install are still gated (written once at scaffold; yours to edit after)
- `.github/dependabot.yml` (Tier-2): Grouped weekly bumps for `gomod` (root + `/gen`), `npm` (first frontend), `docker`, and `github-actions`. Ignores forge and the code generators that stamp their version into generated files — `@bufbuild/protoc-gen-es` with its exact peer `@bufbuild/protobuf`, and `google.golang.org/protobuf` (protoc-gen-go) — because Dependabot cannot regenerate, so their bumps fail `forge ci verify-generated` every time. Bump those by hand with a `forge generate` (written once at scaffold; yours to edit after)
- `.github/CODEOWNERS` (Tier-2): Starter ownership rules (one-shot scaffold; yours to edit after)
- `.github/pull_request_template.md` (Tier-1): Standard PR template

What's enabled is driven by `forge.yaml`:

```yaml
ci:
  provider: github # only "github" today
  # No go_version key: every setup-go step pins `go-version-file: go.mod`,
  # so CI's Go version follows go.mod. Set it there.
  lint: # zero-value = "all enabled"
    golangci: true
    buf: true
    buf_breaking: true # generates proto-breaking.yml
    frontend: true
    migration_safety: true
  test:
    race: true
    coverage: false
  gate:
    extra_checks: [] # project-owned checks for `forge ci gate` — see `ci/gate`
  vuln_scan: # zero-value = "all enabled"
    go: true
    docker: true
    npm: true
  e2e:
    enabled: false
    runtime: docker-compose # or "k3d"
  permissions:
    contents: read # default
```

### Which forge CI installs

CI installs the project's go.mod Forge requirement (or forge.yaml's pin only
when the module has no Forge requirement), never a workflow version literal.
Unresolvable requirements and local `replace` directives fail; no fallback.
The checks job installs pinned codegen tools and runs each frontend's `npm ci`
before verifying generated files. Missing plugins or a pre-dirtied tree must
fail, not certify skipped output. Installation details and troubleshooting:
`forge skill load ci/toolchain`.

### No workflow names a registry

An image registry is part of a WORKLOAD's `image` in `deploy/kcl/workloads.k`
(`image = "ghcr.io/<owner>/<name>"`) and nowhere else. No env declares one, and
no workflow names one — no `REGISTRY` env or repository variable, no value after
`--push`, no `docker/login-action` or `docker/metadata-action` input. The
workflows reach the registries through forge, which derives them from the
images:

```bash
printf '%s' "$TOKEN" | forge registry login <env> --username <user> --password-stdin
forge env build <env> --push                    # each image → its own declared reference
forge registry ref <env> --github-output    # ref= image= digest= for later steps
```

`login` logs in to every distinct host the env's images name, so an env whose
workloads push to two registries needs no extra step.

`build-images.yml` builds once per commit on main for the first deploy env
(the one `deploy.yml` auto-deploys), then signs, SBOMs, attests and scans the
digest-pinned ref `forge registry ref` read back — never a tag and never a
YAML literal. The login credential is `GITHUB_TOKEN` (GitHub's container
registry); for another registry, pipe its credential from a secret (a GAR
key with `--username _json_key`, an ECR token with `--username AWS`).

### A hosted env needs NO registry credential — one token

Where the env pushes to the PLATFORM registry, there is no registry login step
and no registry secret. forge authenticates it with the same
`FORGE_CONTROL_PLANE_TOKEN` the job already holds for the release ledger, and
`forge env build --push` / `forge env deploy` log themselves in before their
first push. So the scaffolded workflows differ by env, not by project:

| Workflow                               | Login step                                                                                                              | Credential                  |
| -------------------------------------- | ----------------------------------------------------------------------------------------------------------------------- | --------------------------- |
| `release.yml` (hosted by construction) | none                                                                                                                    | `FORGE_CONTROL_PLANE_TOKEN` |
| `build-images.yml`, hosted build env   | none for the build; the trivy job runs `forge registry login <env>` with no flags, because it PULLS rather than pushing | `FORGE_CONTROL_PLANE_TOKEN` |
| `build-images.yml`, cluster build env  | `forge registry login <env> --username … --password-stdin`                                                              | `GITHUB_TOKEN` or your own  |
| `deploy.yml` (non-hosted envs only)    | same as above                                                                                                           | `GITHUB_TOKEN` or your own  |

Two things follow, and both are worth knowing before editing a workflow:

- **Do not add `--username`/`--password-*` for the platform registry.** forge
  refuses them for our host, so the job fails on its first run. The flags are
  for a registry you chose.
- **One secret, not two.** A hosted pipeline needs `FORGE_CONTROL_PLANE_TOKEN`
  and nothing else for images. Adding a registry secret gives you two things to
  rotate and only one of them in anybody's memory.

An env that pushes to the platform registry AND one of your own keeps the login
step for yours alone; forge does its half in the same run.

A job that gets a realm **401 / DENIED** on push is almost never missing a
credential. forge composes the push address from the org the token acts for
(there is no `organization` to declare), so check whether the token expired and
whether it carries `deploy:write` — a push needs both the org's subtree and
write access. forge prints a hint saying so. Do not add a `docker login`.

### Deploys go through forge

`deploy.yml` runs, per env, `forge registry login <env>`, `forge build <env>
--push` (each image to the reference its workload declares), then
`forge env deploy <env> --yes` — never `kcl run | kubectl apply`, which cannot
resolve `kcl_plugin.forge` and skips the declared-context binding, the
per-env frontend `config.js` render, digest pinning and the live preflight.

**`--yes` is not optional in CI, and it is not a bypass.** `forge env deploy`
prints the deploy plan and will not write a promotion until somebody approves
it. A GitHub runner has no TTY to prompt on, so without `--yes` the command
exits **5** (`plan_unconfirmed`) having built, pushed and cut but deployed
nothing. `--yes` means "I read the plan" — the plan is still computed and
printed into the job log immediately above the write, so the record of what
shipped is in the run. For a pipeline that wants a human in the loop, run
`--plan-only` in one job and put the approving job behind a GitHub Environment
with required reviewers; `release.yml` already has that shape.
The env list is the project's `deploy/kcl/<env>/main.k` set (dev excluded),
read when the workflow is scaffolded; with several, the first auto-deploys
after a green image build on main and the last is protected. A lone env is
never auto-deployed.

Credentials: an env with `forge.OnCluster` workloads needs `secrets.KUBECONFIG` holding a
context named exactly the ClusterTarget's `cluster`; `forge.OnHosted`
workloads need `secrets.FORGE_CONTROL_PLANE_TOKEN` (for the env's
`forge.ControlPlane`) and no kubeconfig; an env that binds both needs both.
forge picks each workload's path from its own binding.

### Pre-commit and generated files

`.pre-commit-config.yaml` excludes every forge-generated path (`gen/`,
`*_gen.*`, `.forge-kcl/`,
`public/config.js`, the hooks barrel) from the MUTATING hooks — whitespace
fixers, gofmt/goimports, prettier. `forge ci verify-generated` demands
generated files regenerate byte-identically, so a formatter that rewrote one
would fail it on every commit. forge also emits those files already clean
(single trailing newline, no trailing whitespace, `buf format`-clean protos).

## The local gate: `task ci`

CI is a thin wrapper around a gate you run on your own machine. The workflow
prepares an environment and calls ONE task per job, so the same command gives the
same verdict locally:

| Command                 | What it runs                                                                                            | CI job                       |
| ----------------------- | ------------------------------------------------------------------------------------------------------- | ---------------------------- |
| `task ci`               | `ci:checks`, then `test`, then `test:integration`, one summary table                                    | none: run this instead of CI |
| `task ci:checks`        | verify-generated, lint, build, vuln-scan, license, KCL, deployability, plus your `ci.gate.extra_checks` | `checks`                     |
| `task test`             | unit tests + every frontend's                                                                           | `test`                       |
| `task test:integration` | the `integration`-tagged suite                                                                          | `integration-test`           |

Every check runs even after one fails; a check that could not run is `SKIPPED`
and fails the gate unless named in `CI_SKIP`; tools run at the versions CI pins.
**Load `ci/gate` for the rules, the check list, `CI_SKIP`, adding your own checks
(`ci.gate.extra_checks`) and why the workflow has few jobs.**

Heavy lanes (`task test`, `forge generate`, `forge lint`, the gate's heavy
checks) queue for one of the machine's heavy slots instead of piling up; a
`⏳ waiting for a heavy slot` line is a queue, not a hang. **Load `ci/slots`.**

## Tier-1 vs Tier-2 boundary

The forge-generated workflows are Tier-1: they carry the
`# Code generated by forge. DO NOT EDIT.` banner, certify themselves
via an embedded `forge:hash` marker, and are regenerated every
`forge generate`.
Hand-edits get surfaced by `forge project audit` and `forge project map`
(`forge-space, hand-edited (drift from regen)`) and overwritten on the
next regenerate unless you pass `--force` to clobber explicitly or
opt-in to local edits via the workflow comment.

User-owned workflows are Tier-2: forge never reads them, never writes
them, and `forge generate` leaves them alone forever. The boundary is
mechanical:

- **Tier-1** — file matches a name forge knows about (`ci.yml`,
  `proto-breaking.yml`, `build-images.yml`, `deploy.yml`, `e2e.yml`,
  `dependabot.yml`, `pull_request_template.md`) AND carries the
  generated banner. `forge project map` reports `forge-space, regenerated`.
- **Tier-2** — any other workflow file. `forge project map` reports
  `user-owned`. Add as many as you want.

When you need to evolve a Tier-1 workflow beyond what forge.yaml
exposes, the right answer is almost always one of:

1. Add a knob to forge.yaml (and a forge issue / PR if forge can't
   express it yet). This is a forge improvement; surface it.
2. Add the check to the gate with `ci.gate.extra_checks` in forge.yaml
   (see `ci/gate`), or edit `ci.yml` itself: it is
   scaffold-once, so it is yours.
3. Add a parallel Tier-2 workflow file. Keep the file name distinct
   from any Tier-1 name so forge never tries to regenerate it.

## Extending CI

Adding a check (`ci.gate.extra_checks`), a job that needs its own runner, or a
parallel Tier-2 workflow (release, notifications, coverage upload, extra
scanners): **load `ci/extending`**. A new check belongs in the gate, not in a
new job; see `ci/gate` for why.

## `forge ci` helper subcommands

`forge ci gate` runs these as rows of the gate (you rarely call them directly),
and each is runnable on its own:

| Command                        | What it does                                                                                                                                                                                                                                                                                                                       |
| ------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `forge ci verify-generated`    | Runs `forge generate` and asserts nothing changed (`git status --porcelain`, so a newly created file counts) — catches mock/codegen drift where a contract.go grew a parameter but the mock_gen.go wasn't refreshed. Refuses to run when a frontend's protoc-gen-es is missing                                                     |
| `forge ci validate-kcl`        | Renders every environment under `deploy/kcl/<env>/` and asserts the result is APPLYABLE — a `manifests` root exists, every document carries apiVersion + kind, and no other top-level key hides objects no deploy would apply. Shares its implementation with `forge doctor --signal deploy`, so CI and the doctor cannot disagree |
| `forge doctor --signal deploy` | Deployability gate: probes, resource requests/limits, credentials sourced from Secrets rather than literal env values, ServiceAccounts bound to pods, and a way to apply pending SQL migrations. Reads the rendered manifests, so it needs no cluster and no image                                                                 |
| `forge ci vuln-scan --go`      | Runs govulncheck against `./...`                                                                                                                                                                                                                                                                                                   |
| `forge ci vuln-scan --npm`     | Runs `npm audit` in each frontend                                                                                                                                                                                                                                                                                                  |
| `forge ci migration-safety`    | Runs `forge lint --migration-safety` against `db/migrations/`                                                                                                                                                                                                                                                                      |

Run `task ci` locally before pushing to reproduce the whole CI gate.

## `forge lint` is report-only

`forge lint` is a gate, and a gate reports: everywhere — your machine, an
agent's loop, a CI runner — it writes nothing, prints the unified diff each
fix would have applied, and exits non-zero if any is pending. A lint that
rewrote files used to rewrite other people's: in a shared checkout it
reformatted files another PR owned, and golangci-lint's `--fix` turned an
SA9004 finding into a const block that no longer compiled.

`forge lint --fix` applies the mechanical fixes in place, then gates:
gofmt/goimports formatting, the tracing lint's log-call rewrite, prettier and
`eslint --fix`. Every Go rewrite is type-checked afterwards, and a fix that
leaves its package failing is put back and reported. golangci-lint's own
fixes (staticcheck, gocritic, …) are never applied — they are code changes,
so they stay findings for you to make.

| Flags                  | Result                                       |
| ---------------------- | -------------------------------------------- |
| none                   | report-only, non-zero if anything is pending |
| `--check` / `--no-fix` | the same, said explicitly                    |
| `--fix`                | fixes in place (verified), then gates        |

A pending fix is a failing `format` lane in the verdict and in `--gate-json`
evidence, so a pipeline needs no flag to get the strict behaviour. To fix
what the gate reported, run `forge lint --fix` and commit the result.
`--json` is always detect-only, and so are the targeted lanes (`--contract`,
`--db`, …).

## Linting the workflows themselves

Forge has no dedicated CI-workflow analyzer. What covers the workflows today:

- **`forge lint --banners`** — verifies every forge template (including
  `.github/workflows/*.yml`) carries the right Tier-1 / Tier-2 banner.
  Catches a generated workflow that lost its `# Code generated by
forge. DO NOT EDIT.` header or a Tier-2 scaffold missing the
  `# yours: scaffolded once, never touched again` banner. Runs only inside the forge repo
  (the templates live there).
- **`forge project audit`** — reports drift on Tier-1 workflows (forge-space
  files with hand-edits) under the `codegen` category.
- **`forge ci verify-generated`** — re-runs `forge generate` and fails
  if the diff is non-empty. On a checkout where a full `forge generate`
  already verified this exact tree (same forge binary, tools and
  environment) as its own output, the regenerate skips its steps: a tree
  that is a recorded fixed point cannot drift. A fresh clone has no record
  and runs in full; `--no-cache` forces it anywhere.

If you want stricter checks (no `@latest` action versions, required
secrets enumerated, permissions-locked-down rules), file a forge issue
or add a Tier-2 workflow that runs `actionlint` and friends against
`.github/workflows/`. Treat the gap as known.

## Rules

- forge's workflows (`ci.yml`, `proto-breaking.yml`, `build-images.yml`,
  `deploy.yml`, `release.yml`, `e2e.yml`, `reconcile.yml`, `pre-commit.yml`,
  `dependabot.yml`) and the vendored `.github/actions/forge-deploy/action.yml`
  are scaffold-once: written once, then yours to edit.
  Deleting one sticks — `.forge/scaffolded.json` records it, so `forge
generate` does not bring it back. To get forge's current version of one,
  delete it and run `forge project rescaffold .github/workflows/<name>.yml`
  (only workflows this project has: e2e.yml needs an e2e suite,
  reconcile.yml the reconcile feature, release.yml and the forge-deploy
  action a hosted env). Which envs are hosted is read off a render of each
  env; when one does not render yet, rescaffold first writes the config
  modules envs import (`forge generate --steps env-config`), and an env that
  still fails is named — run `forge env render <env>` to see why.
- `forge generate` never rewrites a workflow that exists, so a fix to a
  workflow template reaches your project through `forge project upgrade`:
  `--check` names every workflow whose template has moved on, `--check
<path>` prints that file's diff, and `--force <path>` adopts the template
  for that one file. Workflows are compared only when every env renders;
  otherwise the report names the env that did not.
- Every forge workflow cancels a superseded run on a pull request ONLY:
  `cancel-in-progress: ${{ github.event_name == 'pull_request' }}`. Keep
  that in the workflows you add. A literal `true` on a workflow that runs on
  push to main lets each merge cancel the main run before it — often while it
  is still queued — and on a busy runner pool main goes hours without a
  verdict. A project scaffolded before this rule shows ci.yml (and its
  siblings) in `forge project upgrade --check`.
- Tier-2 workflows live alongside Tier-1 in `.github/workflows/`. Use
  any name forge doesn't own; open with `# yours: scaffolded once,
never touched again — forge will not overwrite this file` so the
  boundary is visible.
- The generated workflows install the forge go.mod resolves, at run
  time. Bumping forge in go.mod is the whole upgrade; there is no pin in
  a workflow to re-stamp.
- `ci.gate.extra_checks` in forge.yaml is the right home for a new CHECK:
  it runs in `task ci` and the CI `checks` job with no extra runner. A job
  that needs its own runner is an edit to `ci.yml`; a standalone trigger
  (cron, tag, dispatch) means a Tier-2 file.
- Adding a job per check is the shape this scaffold moved away from: see
  `ci/gate` ("Why the workflow has few jobs").
- `forge ci verify-generated` is the canonical drift gate. Run it
  locally before pushing if `forge generate` touched anything.

## When this skill is not enough

- **Proto breaking-change handling** — see `proto-breaking`.
- **Deploy and release pipelines beyond CI gates** — see `deploy`.
- **What `forge project audit` reports about CI files** — see `audit-json`.
- **Banner classification details** (Tier-1 vs Tier-2 vs skip-list) —
  see `architecture`.
