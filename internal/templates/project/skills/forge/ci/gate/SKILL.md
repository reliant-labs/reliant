---
name: ci/gate
description: The local CI gate — `task ci` / `task ci:checks` / `forge ci gate`. What each lane runs, the four rules (every failure reported, a skip is not a pass and needs CI_SKIP, pinned tools, the job summary), how checks run concurrently (after, mutates_tree, heavy host slots, --jobs), the verdict cache (`PASS (cached <sha>)`), affected-package unit tests on a branch (CI_FULL), CI_ONLY / CI_REPORT / CI_LOG_DIR, and the full ci.gate.extra_checks schema. Use when a CI job is red and you want it locally, when `task ci` reports SKIPPED, before adding a job or check to CI, or when moving a hand-written gate script onto forge.
---

# The local gate

CI is a thin wrapper around a gate you can run on your own machine. The
workflow prepares an environment (checkout, toolchains, caches) and calls ONE
task per job; the task is the definition of the lane, so the same command gives
the same verdict locally:

| Command                 | What it runs                                                                                                     | CI job that calls it                     |
| ----------------------- | ---------------------------------------------------------------------------------------------------------------- | ---------------------------------------- |
| `task ci`               | `ci:checks`, then `test`, then `test:integration`, in ONE summary table. No change detection: it runs everything | none: this is what you run instead of CI |
| `task ci:checks`        | the static and build lane (below) plus your `ci.gate.extra_checks`                                               | `checks`                                 |
| `task test`             | unit tests + every frontend's                                                                                    | `test`                                   |
| `task test:integration` | the `integration`-tagged suite                                                                                   | `integration-test`                       |

`task ci` and `task ci:checks` call `forge ci gate [checks|test|integration|all]`.
The check list and the tool pins live in forge, not in a script copied into the
project, so a fix to the gate reaches every project on the next forge bump and
CI cannot drift from the local verdict. `forge ci gate --list` names every check
and its lane. For a project with services and a frontend the `checks` lane is:
`deps-<fe>`, `verify-generated`, `golangci-lint`, `buf-lint`, `migration-safety`,
`lint-<fe>`, `typecheck-<fe>`, `css-health-<fe>`, `go-build`, `build-<fe>`,
`vuln-go`, `vuln-npm`, `license-check`, `validate-kcl`, `deployability`. Which
rows exist follows the same forge.yaml toggles the workflow renders from
(`ci.lint`, `ci.vuln_scan`, ...), so a toggle means the same thing in both.
`license-check` judges the licenses of your dependencies, not your own code:
it passes go-licenses `--ignore` for the module in `go.mod` and for the modules
a root `go.work` uses from inside the project, so a project needs no LICENSE
file of its own for the check to run clean.

Four rules every run obeys:

1. **Every failure is reported.** Each check runs even if an earlier one failed;
   a table follows (check, `PASS`/`FAIL`/`SKIPPED`/`N/A`, duration) and the exit
   status is non-zero if any check failed.
2. **A skip is not a pass.** A check that could not run (a missing tool, a dirty
   tree, no network to install a pinned tool) is `SKIPPED` with the reason, and
   fails the gate unless you name it: `CI_SKIP=vuln-go,license-check task ci`.
   The summary still lists it. A name that matches no check is an error, so a
   typo cannot silently skip nothing. `N/A` (no `db/migrations/`, no integration
   tests) is not a skip and does not fail the gate.
3. **Pinned tools.** golangci-lint, buf, govulncheck and go-licenses run at the
   versions the workflow installs (`templates.GolangciLintVersion`,
   `BufVersion`, `GovulncheckVersion`, `GoLicensesVersion`), built into forge's
   tool cache (`$FORGE_TOOLS_CACHE` or the user cache dir) and put first on that
   command's PATH. That cache is shared by every worktree on the machine, one
   entry per tool version, each published by one rename so a concurrent gate
   never runs a half-written binary. A copy on PATH at another version is not used, and
   neither is a golangci-lint built with a Go older than your `go.mod`. Your
   PATH and GOBIN are never touched. `forge tools pins [tool]` prints these
   versions (`--json` for all), so a hand-written workflow reads them from the
   forge it runs.
   buf is the one a project moves itself, in ONE place: a go.mod tool directive
   (`go get -tool github.com/bufbuild/buf/cmd/buf@v1.57.0`). The gate,
   `forge generate`, `forge tools install buf`, `forge tools pins buf` and the
   scaffolded ci.yml then all run that buf instead of forge's — no Makefile pin,
   no `$BUF` on PATH.
   `forge lint` outside the gate does the same for golangci-lint: with none on
   PATH it builds the pin into the tool cache once, and when it cannot, the
   golangci-lint lane FAILS naming `forge tools install golangci-lint`. Likewise
   a missing buf fails the buf lint lane. A gating lane that did not run is
   never a pass.
4. **The table reaches the job summary.** When `$GITHUB_STEP_SUMMARY` is set the
   table is appended to it as Markdown.

`verify-generated` never writes the tree: it regenerates a scratch copy of the
working tree beside the repository (`.<repo>.forge-verify-*`, removed when it
finishes) and diffs the copy against itself, so every change it reports was
made by `forge generate`. It judges the working tree as `git add -A` would
commit it — on a clean checkout, the committed tree — and a dirty tree gets a
real verdict, not a skip.

Change detection stays in the workflow: skipping jobs a docs-only change cannot
affect is a cost optimization, and locally you scope by running one lane (or
`CI_ONLY`).

## Reviewed vulnerability exemptions

`forge ci vuln-scan` fails on called Go advisories and high/critical npm
advisories (lower npm severities are printed). Prefer upgrading the dependency;
for a transitive npm pin, a scoped root `package.json` override can take a fixed
release. npm ignores overrides in published dependencies and workspace members.

If no fixed release exists and the vulnerable path cannot affect the project,
accept **one advisory**, not a package, in the same list for both scanners:

```yaml
ci:
  vuln_scan:
    exemptions:
      - id: GHSA-vfj7-8cjw-p6xm # npm: GHSA id; Go: GO-YYYY-NNNN id
        reason: >-
          No fixed braces release exists. Only the dev lint toolchain uses it,
          with project-owned glob patterns, never untrusted runtime input.
        expires: "2027-01-12" # example only: choose a real review date
```

Reason and expiry are required. Expired/incomplete entries fail the gate;
entries matching nothing are reported for removal. A new advisory in the same
package still fails. Each scanner considers only its ID namespace, so Go entries
do not appear stale to npm and vice versa. Scaffolding Next.js records a
90-day reviewed acceptance for its unfixable dev-only braces advisory in
`forge.yaml`, visibly, rather than disabling the npm scan.

## How checks run

Checks run **concurrently**; the table still lists them in plan order, with
each one's time and the gate's wall time. Three things bound the concurrency:

- **`after`** — a check starts once the checks it names have finished, whatever
  their verdict (rule 1 still holds). `lint-<fe>`, `typecheck-<fe>`,
  `build-<fe>` and `verify-generated` wait for `deps-<fe>`.
- **Tree mutation** — a check that rewrites tracked files (an extra with
  `mutates_tree: true`) runs ALONE, before every check that reads the tree.
  `verify-generated` is not one: it runs beside the readers.
- **Host slots** — a `heavy` check (whole-module build, test run, lint, render)
  holds one of the machine's heavy slots while it runs (`ci/slots`), so the
  gate never adds more heavy jobs than the host has room for, and the commands
  it runs (`forge generate`, `task test`'s `forge slot run`) run inside that
  slot instead of queueing again. Light checks run under `--jobs` (default half
  the CPUs, 2-8). Do not wrap the gate itself in `forge slot run`.
- **Longest path first** — slots go to the waiting check with the longest
  chain still to run: its own duration plus the longest chain of checks that
  wait on it, from the durations earlier runs of this repository recorded
  (`durations.json` beside the verdict cache). With no history, plan order.

Each check's output goes to `tmp/ci/<lane>/<check>.log` (`CI_LOG_DIR` moves it;
`.forge/ci/<lane>/` when git does not ignore `tmp/`). The console carries one
`▶` start and one `✓`/`✗`/`⏭` finish line per check, and a failure's last 25
lines; under GitHub Actions each check's whole output in its own `::group::`.

## Verdict cache: `PASS (cached <sha>)`

On a **committed** tree, a check that already PASSED on this exact tree is not
run again; its row reads `PASS (cached 1a2b3c4)`. The key is the tree hash
(`HEAD^{tree}`), the check's command, dir, env and pinned tools, the forge
binary, forge's tool pins, the Go toolchain (and node, with frontends) and the
Go build env (`GOFLAGS`, `GOOS`…). Inputs outside the tree that the build reads
anyway — a `go.mod` `replace` or `go.work` `use` of a sibling checkout, a
frontend's `file:` dependency — must themselves be clean git trees, whose hash
joins the key; otherwise the cache is off for the run.

- Shared across every worktree of the repository (keyed by its git dir), under
  the user cache dir (`FORGE_CI_CACHE_DIR` moves it). Only PASS is stored, and
  only when the run ends on the tree it started on.
- A **dirty tree turns it off**, and the gate says so in one line.
- `CI_NO_CACHE=1` or `--no-cache` runs everything.
- Never cached: `vuln-go` / `vuln-npm` (advisories change daily), `deps-<fe>`,
  and any extra with `cache: false` (network, sibling checkouts, clocks).

## Unit tests on a branch: affected mode

On a non-default branch, `unit-tests` runs `task test -- <packages>` for only the
Go packages whose test binary (the package, its tests, everything they import)
contains a package the branch changed since `git merge-base HEAD origin/<default>`
— committed, staged, unstaged and untracked changes alike. A changed `.go` file,
a file a package embeds, or a file under a package's own `testdata/` changes that
package. The gate prints the mode and why, e.g.
`unit tests: affected mode — 7 of 212 test packages reach the 4 changed file(s) since origin/main`.

It **falls back to the whole suite** when any other file changed (`go.mod`,
`go.sum`, a `testdata/` outside a package, KCL, proto, migrations, the
Taskfile, scripts — nothing proves which tests read them), on the default
branch, when nothing changed, without `origin/<default>`, and with `CI_FULL=1`
or `--full`. A branch stacked on another names its parent with `CI_BASE=<ref>`
or `--base`. List files no Go test reads in `ci.gate.affected_ignore`
(`["*.md", "docs/**"]`) so they do not force it. A direct `task test` always
runs the whole suite: affected mode is the gate's, for a branch.

## Selecting and recording

| | |
| --- | --- |
| `forge ci gate <lane>` | `checks`, `test`, `integration`, `all` (those three), or a lane your extras declare (`docker`) — never part of `all` |
| `CI_ONLY=a,kcl` / `--only` | just these checks or **groups**, plus what they depend on (`after`); others are not listed. Groups: `setup`, `static`, `kcl`, `test`, `integration`, `extras` (an extra's default), or your own |
| `CI_SKIP=a,b` / `--skip` | run but list as SKIPPED (allowed). An unknown name in either is an error |
| `CI_REPORT=path` / `--report` | also write the Markdown table to a file (for a PR body) |
| `forge ci gate --list` | every check with its lane, group, `heavy`, `mutates-tree`, `after` |

The summary ends with `head: <sha>` (`+ uncommitted changes` when dirty), so a
pasted table names the commit it judged.

## Why the workflow has few jobs

GitHub bills every job rounded UP to the whole minute, and a runner is a fresh
machine: each job repeats checkout, toolchain setup, cache restore and the forge
install before doing any work. When every check was its own job, the cost of a
PR grew with the NUMBER of checks rather than with what they do: the one project
generated from that layout spent roughly half its Actions minutes on setup, about
$850/month at its peak pace, until its org budget blocked its CI entirely. So the
static and build checks share one job behind one setup. To add a check, add it to
the gate (`ci.gate.extra_checks`, below), not a job. Add a job only for something
that needs its own runner (a service container, a different OS).

Main pushes share one concurrency group that never cancels, so every merge ends
in a verdict; PRs cancel a superseded run per ref.

## Adding your own checks

```yaml
# forge.yaml
ci:
  gate:
    env: { GOPRIVATE: "github.com/acme/*" } # every check, forge's too
    path: [.tools/bin]                       # first on every check's PATH (your own pins)
    affected_ignore: ["*.md", "docs/**"]     # files no Go test reads
    extra_checks:
      - name: gen-llm-gateway        # the row; what CI_SKIP / CI_ONLY match; unique
        command: [go, run, ./cmd/gen-llm-gateway, -check]  # argv, no shell; exit 0 = PASS
        after: [verify-generated]    # start once these finished (any verdict)
        heavy: true                  # hold a host heavy slot while it runs
      - name: kcl-prepull
        command: [./scripts/test-kata-prepull.sh]
        group: kcl                   # CI_ONLY=kcl selects it (default group: extras)
        requires: [yq, jq]           # missing => SKIPPED, not PASS
        heavy: true
      - name: migration-chain
        command: [scripts/ci-step.sh, migration-chain]
        probe: { command: [docker, info], reason: "docker daemon not reachable" }
      - name: reliant-vendored-proto
        command: [scripts/ci-step.sh, reliant-vendored-proto]
        skip_exit_code: 77           # exit 77 = SKIPPED, last output line is the reason
        cache: false                 # reads a sibling checkout: never reuse a verdict
        env: { RELIANT_DIR: ../reliant }
      - name: pre-commit
        command: [pre-commit, run, --all-files, --show-diff-on-failure]
        mutates_tree: true           # runs alone, before the readers; reset on failure
        dir: .                       # project-relative working directory
      - name: image-control-plane
        command: [docker, build, -t, app:ci, .]
        lane: docker                 # only `forge ci gate docker` runs it
      - name: lint-go
        command: [scripts/ci-step.sh, lint-go]
        replaces: golangci-lint      # take over one of forge's rows, in its place
        heavy: true
```

If the project pins tools of its own for `path` to find, install each one once
per machine and link it into `.tools/bin`, rather than copying the whole set
into every checkout: a set of linters and code generators is 0.5-1 GiB, and an
agent-heavy machine holds dozens of worktrees. Key the shared install by tool,
version and platform, publish it with one rename, and lock concurrent installs
of the same key, as forge's own tool cache does.

An extra runs in the `checks` lane unless `lane` says otherwise. `replaces` puts
it in the replaced row's position, lane and group; forge's command does not run.
An `after` that names no check, and a dependency cycle, are errors. A
`mutates_tree` check that fails on a tree that was clean when the gate started
resets the tree; on a dirty tree it never does.
