# The merge gate and the release gate

reliant has two local gates. The **merge gate** runs before every merge and
judges only what the change can affect. The **release gate** runs on main
before a release and judges everything. The split is deliberate: **speed was
chosen over catching everything before the merge.** The merge gate aims at
**five minutes of wall time** for a typical PR. Whatever it leaves out runs in
the release gate, and a release can only be cut from a commit the release gate
recorded green.

This mirrors control-plane's `task ci` / `task ci:full`
(`control-plane/docs/local-gate.md`). The one rule set for "what can this diff
affect" is reliant's own `tools/test-affected` (`dev-docs/testing-affected.md`).
Neither gate keeps a second copy of it.

| Tier | Command | What it runs | When |
|---|---|---|---|
| **merge** | `task ci:merge` (`scripts/merge-gate.sh`) | the affected tests (`-short`, cached, no `-race`), `go build ./...`, `forge lint --scope <changed dirs>`, migration-safety when a migration changed, and the forge-generate and docs-generate no-op checks only when one of their inputs changed | before **every merge**. The merge queue runs it on the squash it is about to land |
| **release** | `task release:gate` (`scripts/release-gate.sh run`) | everything: unscoped `forge lint` (golangci over the whole module, buf, the whole-project lanes, the frontends), `task generate:check`, `task generate:docs:check`, `go build`, `go vet ./...`, `task test` (Go `-race -count=1` without `-short`, so every slow test runs, plus web and electron in full), `task test:integration` and `task test:e2e` | on a commit of **main**, before a release. `task release:tag` refuses a commit without a green record |

## The merge gate

```sh
task ci:merge                    # your branch, against its merge-base with origin/main
task ci:merge -- --base <rev>    # a stacked branch
```

It holds **one** host heavy slot for the whole run (`forge slot run`), and every
phase nests inside that slot. It prints one row per phase:

```
### merge-gate: PASSED

| phase | result | seconds | started +s |
|---|---|---|---|
| heavy slot (queue) | - | 41 | - |
| plan | PASS | 14 | 0 |
| generate | PASS | 70 | 14 |
| conventions | PASS | 11 | 84 |
| docs | PASS | 58 | 95 |
| build | PASS | 48 | 153 |
| test | PASS | 96 | 153 |
| lint | PASS | 61 | 153 |

wall: 249s in the slot, after 41s queued for it; diff base 8dec9aeb
```

The first row is the time spent **queued** for a slot. That cost comes from the
host, not from the gate: other agents' builds hold the slots. The five-minute
target is measured on the `wall:` line, which starts once the slot is held.

The phases run in this order:

1. **plan**: `tools/test-affected plan -json`, which decides everything below.
2. **generate** runs only if the plan's forge-generate lane is selected. It runs
   `task generate:forge`, the same command `task generate:check` runs, followed
   by `forge lint --conventions`. Your branch must arrive with its generated
   output committed. In the merge queue's `--amend` mode, a regenerate that
   changes only generated paths is folded into the squash. That happens when
   merging main left a generated file a step behind. A regenerate that changes
   any other file fails the gate.
3. **docs** runs only if the plan's docs-generate lane is selected. It runs
   `task generate:docs` under the same no-op rule.
4. **build**, **test**, **lint** and **migsafety** then run at the same time:
   - **build** is `go build ./...`.
   - **test** is `tools/test-affected run -lanes go,web,electron`.
   - **lint** is `forge lint --scope <every changed directory>`, or unscoped
     when `go.mod`, `go.sum` or `forge.yaml` changed (a pin bump moves every
     package).
   - **migsafety** is `forge lint --migration-safety`, and runs only when a
     migration changed.

generate and docs come first because they can change the tree that the later
phases judge.

### What the merge gate does not catch

These are the trade-offs. Each is caught by the release gate:

- **Slow tests.** A test marked `testkit.Slow` (or an older
  `testing.Short()` skip) never runs before a merge.
- **The race detector.** Nothing before a merge runs `-race`.
- **Whole-project lint lanes.** golangci over packages the diff did not touch,
  the frontend lint, the graph, component-drift and config-reach lanes cannot
  be scoped, so `--scope` skips them.
- **Semantic interactions with a concurrent merge.** When main moves during the
  gate, the queue lands without re-running the gate if main's new commits touch
  none of the branch's files and none of the project-wide inputs: pins, proto,
  migrations, the Taskfile, the selector. In that case it only re-checks that
  the result builds. Two such changes can still interact through behaviour,
  with each one green alone.
- **Integration-tagged and e2e tests.**

## Slow tests: what moved to the release gate

A test that takes more than about two seconds under `-short` is a **slow test**.
It calls `testkit.Slow(t, "<what makes it slow>")` first
(`github.com/reliant-labs/forge/pkg/testkit`). The call skips the test under
`-short` and in the integration lane, so the test runs once, in `task test` /
the release gate. Gate a slow test; never weaken its assertion. If a test is slow
only because it waits out a production timeout, make it fast instead: inject the
duration through a package-level var or an option.

The first sweep (2026-10-24) found the slow tests by measurement. It ran
`go test -short -json -count=1 ./...` over the whole module at a host load of
about 100 on 32 cores. Every test that took 6s or more (about 2s on an idle
machine, given the roughly 3x contention) was then re-run on its own. The 325
tests that took 2s or more in both runs were gated. One test (cliauth, 14.5s in
the first run and 0.02s in the second) was a contention false positive and was
left alone.

| package | tests gated | what makes them slow |
|---|---|---|
| `internal/worktreereclaim` | 51 | real git repositories, git subprocesses |
| `internal/db` | 46 | many round trips to pgtest's Postgres |
| `internal/handlers/trigger` | 41 | DB-backed handler tests |
| `internal/workflow/runtime/activities/handlers` | 38 | activities against pgtest's Postgres |
| `internal/worktreesweep` | 33 | real git worktrees |
| `internal/handlers/worktree` | 26 | git through the handler |
| `internal/toolexec/daemonruntime` | 24 | real checkouts and worktrees on disk |
| `internal/provetest` | 24 | the fix's test runner and git as subprocesses |
| `internal/threads` | 18 | DB-backed thread trees |
| `internal/handlers/chat`, `gitutil`, `ghdelegated`, `pid1`, `llm/tools`, `handlers/inbox`, `launch`, `scenario/runner`, `catalog/github`, `daemon` | 24 | the same causes, a few tests each |

So a DB-backed or git-backed test that is fast still runs on every merge. One
that is slow runs before every release.

## The release gate

```sh
task release:gate                       # origin/main, in the background: 40-70 min on a loaded host
scripts/release-gate.sh run <sha>       # a specific commit of main
scripts/release-gate.sh check [<rev>]   # 0 green | 1 red | 3 no record
scripts/release-gate.sh latest          # newest green commit on main
```

The gate runs in a **private detached checkout** of exactly the commit it
judges (`~/.cache/reliant-release-gate/<clone>/snap`). That way your checkout
can be dirty, behind or on a branch, and main can keep merging during the 40-70
minute run, and neither changes what the gate judges. It holds one heavy slot
for the whole run. Every step runs even after a failure, and the run ends with
one table.

**The record.** Each run writes two kinds of record:

- `<git-common-dir>/release-gate/<sha>.json` and `.md`. These live outside
  every tree and are shared by every worktree of the clone.
- A git note on the commit under `refs/notes/release-gate`, pushed to origin.
  A release cut on another machine finds this one.

A record names its steps, their seconds, the forge pin and the host. A run of
only some steps (`RG_ONLY=lint,test`) is never recorded.

**The release cut.** A release has three steps:

1. `task release:rc` (or `:patch`, `:minor`, `:major`) opens the version-bump
   PR. Merge it.
2. `task release:gate` (or `scripts/release-gate.sh run <the bump's sha>`).
3. `task release:tag` tags the bump commit. It runs
   `scripts/release-gate.sh check` on that commit and refuses without a green
   record.

A green recorded on a first-parent ancestor (up to 50 commits back) also counts
when every change since it is release metadata: docs, `*.md`, or a change to
`electron/package.json` / its lockfile whose only difference is the `version`
field. So you can run the gate on main before opening the bump PR. As long as
nothing else merges in between, the bump commit inherits that green.

A hotfix that cannot wait takes `scripts/release-tag.sh --skip-release-gate`.
Say so in the release notes. There is no waiver for a red run: fix it.

## The merge queue

The fleet's merge queue (`.fm-tools/merge_queue3.sh`, outside this repo) is a
thin orchestrator over `scripts/merge-gate.sh`. For each branch, in parallel, on
its own pool checkout, it:

1. merges main in (never a rebase);
2. re-versions the branch's migrations above main's;
3. squashes onto main;
4. runs `scripts/merge-gate.sh --base <main> --amend` in one heavy slot.

Only the land step is serialized under one lock: fetch, push and
squash-merge. If main moved, the queue carries the verified commit onto the new
main and re-checks only that it builds, as long as main's new commits are
disjoint from the branch's files and from the project-wide inputs. Otherwise it
runs the gate again. The queue's summary, posted into each merge PR's body,
separates the time queued for a heavy slot from the time spent running.
