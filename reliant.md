<!--
  forge repo notes. Reliant injects forge's live framework guide
  (internal/templates/project/reliant.md.tmpl — architecture, critical rules,
  package-boundary idioms, testing basics) ahead of this file because this repo
  has a forge.yaml, so none of that belongs here. Keep this file to what is true
  of working ON forge itself.
-->

## ⛔ You are running INSIDE forge — do not kill it

The agent session reading this is itself hosted by a forge process. Commands
that stop forge, or that match processes by name, therefore terminate **your
own session** and every agent working alongside you. All in-flight work stops,
and nothing gets reported back.

Never run any of these:

- `pkill forge`, `killall forge`, `pkill -f forge`, or any pattern-matched kill
- `kill` against a PID you did not personally start

`forge env down <env>` / `--all` will not stop the process running it or any
of its ancestors — it walks its own parent chain first and reports what it
skipped — so it cannot end your session directly. It still stops every OTHER
stack it reaches, including ones other agents depend on: run `--all` only when
you mean every stack on the machine, and prefer `forge env down <env>` from
inside the project you started.

**To clean up, be surgical.** Remove only containers you created, by explicit
name (`docker rm -fv <name>` — `-v` also removes the container's anonymous
volumes; without it every image that declares a `VOLUME`, postgres included,
leaves one behind). Stop only a PID you started yourself and recorded.
Leave every pre-existing container, process and stack alone — a shared dev box
runs many of them, and another agent almost certainly depends on one.

**Leaving a stack running is the correct outcome.** It costs a little memory. A
pattern-kill costs the session, the other agents, and the work.

## Philosophy

> Forge is your LLM's best friend. It aims to scaffold a production app from day
> 0, with hooks and seams that allow the LLM to introspect, and connect your app
> to best practices like middlewares, deployments, env promotion, monitoring and
> more. It must support the happy-path 80/20 rule: 80% of users get out of the
> box happy path, but the last 20% of users are never disempowered. We provide
> escape hatches and primitives for users to build upon.

Read this before proposing a design. It rules out whole categories of answer:

- **Production from day 0, not a toy that graduates.** A scaffold that works
  only in dev, and must be replaced before shipping, has not saved anyone work
  — it has deferred it to the moment they can least afford it. What forge
  scaffolds on day 0 is the same thing that runs in prod, configured
  differently.
- **Declarative over imperative.** The state of the world is DECLARED in files
  under version control, and something converges reality to it. A setup step
  that must be _run_, in order, against a live system, is a step that can be
  skipped, half-completed, or lost when a teammate clones the repo. If a
  provisioning story requires a CLI invocation, that is a smell to be designed
  out, not documented around.
- **Primitives, not modes.** An enum of three blessed configurations is not a
  primitive; it is three products, none of which is the one a given user needs,
  and it leaves dead branches in every project that picked one. Ship the real
  thing wired up, and expose the seams that let it be rewired.
- **Working defaults, not empty ones.** A scaffolded field left blank is not
  neutral — it is a broken app plus a scavenger hunt. Scaffold values that make
  the thing RUN, and make replacing them a one-line edit in a file that already
  exists.
- **The 20% are never disempowered.** Every default is overridable, every
  generated file has an owned seam beside it, and no convenience is implemented
  as a wall.
- **Never stand in for another tool.** forge never parses, interprets or
  emulates a Dockerfile, or any other tool's input format, to do that tool's
  job: a re-implementation drifts from the real semantics and becomes a second
  builder to maintain. If docker or buildx is missing, fail with a clear error
  naming the fix.

## Package boundaries: the worked example in this repo

The framework guide's package-boundary idioms apply here first. Concretely:
`templates.Render` serves eleven unrelated template categories. It knows only
the one-method `selfDefaulting` interface, declared at the consumer — not any
payload's shape. An earlier version type-asserted one concrete struct and
reached into another domain's package, which made the shared renderer the
obvious place for every domain to special-case. See
`internal/templates/self_defaulting_test.go`, which pins the boundary rather
than the behaviour. A reach for a third interface method is the signal to ask
whether you are modelling the consumer's need or the implementation.

## Never edit CHANGELOG.md in a pull request

`CHANGELOG.md` is written **only at release time, by `scripts/release-forge.sh`**,
from the titles of the pull requests merged since the previous tag, and the same
section becomes the GitHub release notes. Nothing reads it per PR. Every PR that
appended under `[Unreleased]` conflicted with the next one in the merge queue, so
`ci.yml`'s `checks` job fails a pull request whose diff touches it
(`scripts/check-changelog-untouched.sh`; `release/*` branches and the
`chore: release vX.Y.Z` commit are exempt).

So the PR title is the changelog entry, and it also sets the version. Write a good one, in conventional-commit form:

- `feat(scope): what a user can now do` → **Added**, and the release is a **minor**
- `fix(scope): what no longer breaks` → **Fixed**, a patch
- anything else (`refactor`, `docs`, `perf`, `chore`, …) → **Changed**, a patch
- a breaking change (`feat(scope)!: …`, or a `BREAKING CHANGE:` footer in the commit
  body) is **not allowed in v1**. The release train and `release-forge.sh` refuse to
  cut a release that contains one, and name the PR (see below). The **Breaking
  changes** section exists only for a major release, and only the owner cuts those.

A doc comment that says "record it in CHANGELOG.md" means: say it in the PR title.

## Semver at major 1: deprecate, never break

Owner decision, 2026-11-05: forge follows real semver, and its major stays at 1.
`scripts/semver-next.sh` (the same file in reliant and control-plane) computes each
release's version from the PR titles since the last final tag. Any `feat` makes
the release a minor (`v1.Y+1.0`); anything else makes it a patch. `NEXT_VERSION`
names the next minor, and the lint rollout table, codemods and playbooks are keyed
to it (`docs/releasing.md`).

A public surface never changes incompatibly within v1. The public surfaces are the
`pkg/` Go API, CLI commands, flags, exit codes and `--json` document shapes (pin bots
and consumer scripts branch on them), the `forge.yaml` and KCL schemas, the proto
options under `proto/forge`, generated-code contracts, and lint rules a consumer's
gate runs. To retire something:

- keep the old path working beside the new one;
- mark it deprecated (`// Deprecated: use X.`, `[deprecated = true]`, a hidden flag
  that warns and names the replacement), for at least 2 minors;
- leave its removal to a future major.

A major (v2) stays possible, and only the owner cuts one. forge is upgraded by
agents, so a major's contract is its upgrade path, not stability: every break it
carries ships a `migrations/<version>` playbook that `forge project upgrade list`
surfaces, with the mechanical part as a codemod (or playbook `automation:` step)
that `forge project upgrade` applies itself, and the rest written as steps an agent
can follow. The consumer-facing statement of this policy is the `migration-upgrade`
skill's "Compatibility policy"; keep the two in step.

The gates hold this line. `internal/apigate` fails an incompatible `pkg/` change
that the PR does not declare (the `api-break` label or an `API-BREAK:` line) and
ship a codemod or migration playbook for; pr-review treats any break as a blocker;
and `scripts/semver-next.sh` refuses to cut a release over a breaking title. A new
or stricter lint rule is not a break, because it still warns before it gates
(below).

## Format with `task fmt:web`, never a bare `npx prettier`

`npx prettier` resolves whatever prettier release your machine has, not the one
the pre-commit hook pins, and a different release reformats lines you never
touched — the Checks job then fails on them. `task fmt:web` formats the files
you changed since `origin/main` (`task fmt:web -- <file>...` for named ones,
`-- --check` to report) with `scripts/prettier.sh`, which takes the version AND
the file selection from the prettier hook in `.pre-commit-config.yaml`, the one
place both are set (held equal to `templates.PrettierVersion` by
`TestPrettierVersionIsPinnedEverywhere`).

## New lint rules warn before they gate

A lint rule or lane that joins forge, or a severity raised from warning to
error, must not turn a consumer's gate red the day they pin the release that
ships it. Every rule `lint.rules` can grade has a row in
`internal/cli/lint/lint_rollout_table.go`: `IntroducedIn` is the release it
ships in (the one after `VERSION`), `GatesFrom` is the release after that. A
project whose `forge_version` is below `GatesFrom` sees the rule's findings as
warnings carrying a "new in vX; gates from vY" note, in every output mode; at
`GatesFrom` they gate. `lint.rules` still wins (`{rule: error}` opts in early,
`{rule: warn}` extends the grace period).

`TestEveryGradableRuleDeclaresItsRollout` fails a rule with no row, so a new
lane cannot land without choosing its date. Tightening an existing rule is the
same act: move its ID from `shippedRules` to `newRules` with the tightening
release. A rule that must gate on arrival is the owner's call, not yours: it
breaks every pinned consumer, so ask first. Mechanism and rules:
`internal/cli/lint/lint_rollout.go`; the consumer-facing description is in the
`ci` skill.

## Testing tiers

Run the cheapest tier that answers your question. Wall-clock budgets are
enforced conventions — if you add a test that breaks a budget, gate it. Most of
an agent's test time was measured as compile/link plus defeated caching, not
test bodies.

1. **Inner loop — every edit:** `task test:short -- ./internal/<pkg>/...`, naming only the packages you touched (`go test -short`, cached, no `-race`).
2. **Package-targeted — before committing:** `task test -- ./internal/<pkg>/...`. Full mode (no `-short`, with `-race`) for the packages you touched, so the gated slow tests run too.
3. **Full gate — once, at the end / main's CI:** `task test` — the whole module in main's three CI lanes from `scripts/ci-test-packages.sh` (unit and `internal/cli` under `-race -count=1`, the guards at `-p 1` without `-race`), each with its own hang budget (15m / 45m / 30m) — plus the e2e corpus: `task test:e2e` (`go test -tags e2e -count=1 -timeout 60m -run '^TestE2E' ./internal/cli/`; narrow it with `task test:e2e -- -run <regex>`). The e2e tests are `t.Parallel()` (independent projects in separate temp dirs, forge binary built once via `sync.Once`), so wall-clock is roughly the slowest fixture. Run it in the background: the full lane, `internal/cli` / `internal/tierguard` in full mode, and the e2e corpus all exceed ~2 minutes.

Rules that keep the tiers honest:

- Any test that takes **>2s** (subprocess spawns, git repos, network, real scaffolds, KCL renders, `go build`/`go list`/`go mod tidy`, `npm install`) must be skipped or have its slow side-effect bypassed under `testing.Short()` — `if testing.Short() { t.Skip("<why it is slow>; runs in task test") }` — with the slow path still exercised in full mode and CI. Never weaken an assertion — gate, don't gut. A test that is slow because its fixture waits on something it never needed to (a fake CLI that sleeps, a real `gcloud` call) is a fixture bug: fix the fixture instead of gating.
- **A test that runs under `-short` must be cache-sound.** Go's test cache keys only on what the test PROCESS opens or stats inside the module. Repo files read by a CHILD process (`go build`/`go list` over repo packages, `git` against the checkout, `kcl`/`node`/`bash` over files on disk) or by the native KCL runtime are invisible to it, so the cached run replays a stale PASS after an edit that breaks the test. Such a test either skips under `testing.Short()` or opens/stats those inputs itself (a `filepath.WalkDir` + `os.Stat` over the tree is enough — verified). Inputs that are `go:embed`ded into the test binary, or written by the test into `t.TempDir()`, are already covered.
- A package that exceeds `task test:short`'s 300s timeout has grown a slow test. Find it with `go test -short -count=1 -json ./internal/<pkg>/` and gate it; do not raise the timeout.
- e2e tests that boot servers must allocate ports with `freePortE2E(t)` (`internal/cli/scaffold_e2e_test.go`) — never hard-code a port; the corpus runs in parallel.
- e2e tests must keep all state inside their own `t.TempDir()` project; no `t.Setenv`/`t.Chdir` in parallel tests (Go panics on the combo).
- **Flaky = delete immediately; rewrite as a new deterministic test if it's worth having** (owner, 2026-11-01: "a flaky test is a broken test, and is less useful than if we just deleted that test"). The flake gate keeps a NEW flaky test out (each added or edited test runs three times). An EXISTING test that fails intermittently is deleted, not root-caused — the test, or just its flaky assertion — in the same PR that finds it, with an issue labelled `flake:removed` holding the test body and the failure signature. A failure the host causes (inotify or fd budget, disk, a registry download) makes the test `t.Skip` with a message naming the missing resource, never fail. Never re-run Actions to green: the `Flake report` workflow on main reruns a failed run's tests once and files a `flake:suspected` issue for each that then passes, without turning anything red.
- Never set a private `GOMODCACHE` either. A pull request's CI runs the `-short` tier (no `-race`); a push to main runs the full non-short suite with `-race`. So tier 2 before committing is how a gated test of yours gets exercised before it merges.

See the comment block at the top of `internal/cli/fixture_corpus_e2e_test.go` for the same tiers from the e2e corpus's point of view.

### CI: what runs where

Review first, then three moments (owner, 2026-10-30: no CI minutes on a head nobody has
reviewed). A **pull request push** runs almost nothing; the **merge queue** runs the fast
merge tier once, on main plus the entry, and that run decides the merge; a **push to main**
runs the slow tier after the merge, and a red there never blocks a merge — fix it from the
open `Nightly CI is red` issue; a release never ships a forge commit whose full tests are
not green. Each workflow's header says why its lanes are shaped the way they are;
read it before moving a check between lanes. `ci.yml`'s `CI gate` job prints every job's
wall time and the critical path on each run — watch it when you add a test.

**The rule for the merge tier: a check that cannot fit the budget is not on it** (a
five-minute median, ten at worst). A slow test skips under `-short` and runs on main; a
slow job (a linter, a scanner, another OS) runs on main. Every job the queue adds is
another runner to queue for (the org runs 20 jobs at once, and was saturated half of
2026-10-21), so add a step to an existing job before adding a job.

**Pull request push** (`ci.yml`, no path filter; ~2 runner-minutes):

- **Pull request checks** (`pr-checks`) — the CHANGELOG.md guard and the pkg/ API gate
  (`internal/apigate`), the two checks that read the pull request itself (its branch name,
  labels and body). Neither can go red for anything but the pull request's own diff.
- **Review marker** — a notice; enforced in the queue (below).
- `CI gate` is green as soon as those pass, which is what lets GitHub enqueue the pull
  request once `pr-review` passes it. The reviewer reads the diff, not CI results.
- **`ci:run`** runs the whole merge tier on the pull request too, read on the next push
  (an empty commit will do; `ci.yml` does not trigger on `labeled`, because a label run
  would replace the head's `CI gate` verdict). `ci:e2e` and `ci:canary` still start their
  own workflows on `labeled`.

**Merge-queue entry** (`merge_group`; the merge tier, 5 jobs):

- **Test** — three entries: the `-short` tier, no `-race`, as 2 package shards
  (`scripts/ci-test-shard.sh` over `scripts/ci-test-packages.sh short I N`), and the
  **flake gate**: each test the change adds or edits runs three times
  (`scripts/ci-changed-tests.sh`, `-short -count=3`; `-race` joins it on main). A test that
  fails one run in three never merges. The guards' whole-repo work skips under `-short` and
  runs on main. `internal/ciguard` fails if the matrix misses an index. No `-count=1`: an
  unchanged package replays main's result, which works because `test-short-warm` runs the
  same command in the same environment, and `scripts/ci-normalize-mtimes.sh` stamps every
  tracked file with a content-derived mtime (Go's test cache keys files on mtime, not
  content).
- **Checks** — `forge ci verify-generated`, `forge ci validate-kcl`, the pre-commit hooks
  (go-vet-mod skipped), and the Go and npm dependency-license allowlists.
- **golangci-lint (changed packages)** (owner, 2026-11-08; #1947) — its own job, beside
  Test and Checks so it is never on their critical path:
  `scripts/ci-golangci-changed.sh HEAD^1`, the pinned golangci-lint (`install-release-tool`)
  over the packages the entry changed, root `.golangci.yml` and, under `pkg/`, `pkg/.golangci.yml`, reporting the
  findings in the files it changed (`--new-from-rev --whole-files`). A typical diff lints in
  well under the tier's other jobs; `internal/cli` is the slow case. Run it locally with
  `scripts/ci-golangci-changed.sh` (base: the merge base with `origin/main`; `--plan` lists
  the packages). Packages the diff did not touch stay with **Lint** on main.
- **Review marker** — every pull request in the group must carry `pr-review`'s pass marker
  for its current head (`scripts/ci-review-marker.sh`), so a head pushed after a pass cannot
  ride an armed auto-merge in.
- **The same tier, locally: `task ci`.** The merge tier plus the pull request checks in
  about five minutes (`scripts/ci-premerge.sh`): the whole `-short` tier, the flake gate,
  the checks and the changed-package golangci-lint above, concurrently, one table, non-zero on any FAIL or unwaived SKIP.
  `PR=<n> task ci` also reads the pull request's labels for the pkg/ API gate. It is not a
  pre-merge ritual for contributors (GitHub's merge queue gates every PR): verify your
  change with `task test:short -- <your packages>`. `internal/ciguard` holds it to `ci.yml`.
- `CI gate` is the main ruleset's required check, and `main` merges through a merge queue:
  each queued pull request runs this tier (`merge_group`) over main plus the entries ahead
  of it, and lands only if `CI gate` is green there; it also fails if a merge-tier job
  skipped there. A failure dequeues the pull request, which the pipeline's fixer picks up.
  So `ci.yml` has no path filter (a skipped workflow never reports the required check), and
  a job says when it runs with one of the `if:` spellings in its header; `internal/ciguard`
  holds them, and fails a job that would run on every pull request push before review.
- **Merging: open the PR non-draft when it is ready, then stop.** Add no queue label
  (`merge:queue` is the pipeline's, never an author's) and report the PR URL as done. A
  draft means "not ready" and is never reviewed. The Reliant workflow `pr-review`
  (control-plane `.reliant/workflows/`, design in control-plane `docs/pr-pipeline.md`)
  reviews the exact head and posts a COMMENT review plus **`review:passed`** or
  **`review:changes`**. A pass adds `merge:queue` and enters GitHub's native merge queue
  at that exact head, where `CI gate` must pass on the merge group; a refused enqueue is
  labelled **`merge:failed`**. On `review:changes` or `merge:failed`, fix and push: every
  push is reviewed again (pr-fix takes over a PR nobody is fixing). `hold` keeps a PR out
  of both queues. Never `gh pr merge` yourself, never `--admin`, never rerun Actions to
  see green.
- **Releasing: the `rc-train` workflow, never a chat.** control-plane's `rc-train` cuts a
  release candidate when a main moved and 2h passed, or right after a PR labelled
  **`release:asap`** merges: add that label to ship sooner. It tags a commit `main`
  already has and lands the release bookkeeping (`VERSION`, `CHANGELOG.md`) as a
  `chore: release vX.Y.Z` PR; nothing pushes to `main`. Never run
  `scripts/release-forge.sh` yourself unless the owner asked you to release.
- **GitHub identity (owner, 2026-10-30): implement as the user, review and merge as the
  bot.** Commits, pushes and opening a PR are the user's (plain `git`/`gh`). Every review,
  label, enqueue and merge is `reliant-labs[bot]`'s: `reliant github gh --repo
reliant-labs/forge -- <gh args>` (or `reliant github token --as app --repo
reliant-labs/forge`; reliant #1923). Never review, label or merge as `reliant-01`: its
  ruleset bypass is the owner's alone.

**Push to main** (and `workflow_dispatch` on any branch) adds:

- **Unit** — `ci.yml` Test: `go test -race -count=1` over `scripts/ci-test-packages.sh unit`
  (every package except `internal/cli` and the guards), full mode.
- **internal/cli** — `ci.yml` Test (internal/cli): the package alone, `-race -count=1`. Its
  subpackages stay in Unit.
- **Lint** — golangci-lint over the whole root and the whole of `pkg/` (under
  `pkg/.golangci.yml`), `go vet ./...`, and `go vet -tags e2e` over the e2e-tagged packages.
  The merge tier lints only the changed packages; this finds what a change broke elsewhere.
- **Test (-short whole tier, then guards)** — the merge tier's tests in one job, which saves
  the `go-build-short` and `go-mod` caches the merge-tier jobs restore
  (`.github/actions/restore-go-caches`; Checks saves `go-build-tools` on main, which
  Build and the e2e workflows restore);
  then the guards (tierguard, removalguard,
  deadcodeguard, writeguard; `-p 1`, full mode, no `-race`), whose whole-repo work the
  merge tier skips.
- **Build (-trimpath) + go-licenses + govulncheck** — the release build flags, the second
  license opinion (Checks covers dependency licenses on every merge-queue entry),
  `forge ci vuln-scan --go`, the ClickHouse integration tests of `pkg/obsquery` and
  `pkg/alerting`, and every other package's integration tests (db squash, OpenBao with
  envtest, docker builds, the scaffolded MinIO and ClickStack, pgstore, blobstore,
  migratekit; `-tags=integration`, throwaway containers; under `CI` a missing docker
  fails them, `testdocker.Unavailable`). A step that narrows with `-run` must select
  every integration test of the packages it names. `internal/ciguard` fails a
  package with integration-tagged tests that no push-to-main step runs, unless
  `integrationNotInCI` says why.
- **Windows** — native: build, vet, the `-short` tier of Unit + internal/cli, and a
  scaffold+render smoke. (The merge tier does not cross-compile for Windows.)
- **e2e** — `e2e-suite.yml`: `prepare` builds the e2e test binary, forge and the codegen
  plugins once and plans 4 shards by recorded duration (`.github/e2e-timings.json`,
  `internal/e2eshard`); the shards run only `TestE2E*` tests, minus `TestE2ENightly*`; a
  coverage ledger proves they ran every selected test. **E2E Scaffold** (`e2e-scaffold.yml`)
  likewise. Label a pull request `ci:e2e` to run both on it before merging.
- **Nightly** — `nightly.yml` (schedule + dispatch): the full windows/arm64 `go vet`, the
  whole e2e suite (`select: all`, so `TestE2ENightly*` too) and E2E Scaffold. A red nightly
  on main opens, or comments on, the "Nightly CI is red" issue; the next green one closes it.

So a test gated with `if testing.Short()` runs on main, not in the merge queue: mark the
slow ones, and a red main is bisected from the merge that caused it. A red main does not
block merges; it blocks the next release until it is fixed.

e2e naming is load-bearing: a test in an e2e-tagged file must be named `TestE2E*` and no
untagged test may be — `internal/e2eshard`'s guard fails the test shards otherwise, because
the e2e shards select by that prefix. A test with no recorded duration is planned at the
median; `scripts/e2e-timings.sh` refreshes the file from a CI run.

A check moves off the merge tier only when it is slow AND a failure is rare and cheap to
bisect from main. Say why in its job comment.

**Fewer, larger jobs, and caches that fit.** The org runs at most 20 jobs at once across
every repository, so a job is a queue slot as well as ~45-60s of setup: checks that share
a toolchain share a job (`ci.yml`'s header lists what joined what). The repository's cache
budget is 10 GB; every scope saved on main is trimmed first to the entries that run used
(`.github/actions/trim-go-build-cache`), or the scopes outgrow the budget and evict one
another. Add a job, or a cache scope, only when sharing one is worse.

**Test-cache inputs:** `scripts/ci-go-test.sh` removes only `FORGE_SLOT_HELD` before
starting Go. A heavy-slot lease changes on every gate invocation; unsetting it in
`TestMain` is too late because Go hashes the parent's original environment. The
shards and changed-test gate share this wrapper. Repository-wide guard scans use
`internal/repowalk`: it tracks the root's entry-set changes and scanned subtrees,
not the metadata of skipped `.git`/`.forge` state. Its full-mode regression test
runs cmd/go repeatedly and proves both replay and invalidation on real input edits.

Build caches: a merge-tier job restores main's cache and never saves one; only pushes to main
save (`ci.yml`'s "Build-cache note"). A PR's cache is visible to that PR alone, and PR
saves were evicting main's entries from the repository's 10 GB budget. Main saves four
build scopes, each from one job: `short` and `tools` (what a pull request restores) on
every push, `test` and `windows` (plus e2e-prepare and the canary) once per UTC day
(`restore-go-caches`' `refresh: daily`). Every other job restores one of those. When every
job saved its own scope on every push (~9.5 GB a push), main's saves evicted the pull
request's caches and Checks ran cold (400-620s against ~185s), so a new scope needs a
reason.
