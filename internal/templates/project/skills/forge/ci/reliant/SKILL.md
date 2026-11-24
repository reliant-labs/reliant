---
name: ci/reliant
description: CI on Reliant (`ci.provider: reliant`) — the Reliant workflows forge writes instead of GitHub Actions (.reliant/workflows/ci.yaml running `forge ci gate`, and the opt-in agent review.yaml), each posting a GitHub check run as the Reliant App. Switching an existing project, activating the triggers, requiring the check in a branch ruleset, forks and untrusted authors, and adding deterministic or agent checks of your own. Use before moving a project's CI off GitHub Actions, or when a Reliant check is missing, pending or red.
---

# CI on Reliant

`ci.provider: reliant` in forge.yaml makes forge write the project's CI as
Reliant workflows instead of GitHub Actions. GitHub sends the Reliant App its
events (pull requests, merge-queue groups, pushes); a workflow runs on your
Reliant machine and the engine posts the verdict as a GitHub **check run**,
written by the Reliant App. No Actions job runs, no runner is registered, and
no Actions minutes are spent on the merge path. `github` stays the default.

## What forge writes

Both are scaffold-once and yours, like `ci.yml`: forge writes each once and
never again (`forge project rescaffold <path>` re-creates a deleted one).

| File | Check run | What it does |
|---|---|---|
| `.reliant/workflows/ci.yaml` | `<project>/ci` | Runs `forge ci gate --tier <tier> --json` on the commit and maps the `forge.ci.gate/v1` document to the check: a table of every check, the failed checks' notes and log tails, and their findings as annotations on the diff (at most 50). |
| `.reliant/workflows/review.yaml` | `<project>/review` | Opt-in. An agent reads the pull request through GitHub (no machine, no checkout) and posts pass, or failure with each defect annotated. |

`ci.yaml`'s triggers and the gate tier each runs (`ci/gate`, Tiers):

| GitHub event | Tier |
|---|---|
| `pull_request` opened, synchronize, reopened, ready_for_review (drafts skipped) | `pr`, affected tests from the PR's base |
| `check_suite.requested` on `gh-readonly-queue/...` (a merge-queue group) | `queue`, against the group's base |
| `push` to the default branch | `main` |

There is no Re-run trigger. GitHub's Re-run on a check names only a commit
and a branch, and no read a workflow has says whether that commit is a
trusted pull request's head, so a re-run could gate code the pull-request
filter never admitted. To run the gate again, push to the branch (`git commit
--allow-empty`) or close and reopen the pull request: both go through the
filter again. A merge group re-runs by re-queueing.

The workflow only says when and where. What the gate checks is forge's
(`forge ci gate` plans from forge.yaml), so it improves with a forge bump,
not with edits to the YAML. The check's conclusion is how the run ended:
success when it completes, failure when it reaches the `outcome: failure`
node, failure titled "Reliant could not run this check" on an engine fault,
never neutral or skipped.

## Switching an existing project

1. Set `ci: {provider: reliant}` in forge.yaml and run `forge generate`.
   It writes the two workflows and prints the next steps. Your
   `.github/workflows/` files are yours and are left as they are; forge
   stops offering them in `forge project upgrade` and `rescaffold`.
2. Commit `.reliant/workflows/` and push it to the default branch.
3. Enable `ci` on this project's machine: **Workflows → Library → ci →
   Enable**, picking the repository; or
   `reliant trigger activate --workflow ci --machine <machine> --param repository=<owner/name>`.
   Declaring triggers fires nothing until then. The activation must write as
   the Reliant bot (the default): only an App can write a check run.
4. Once `<project>/ci` reports on a pull request, require it (below), then
   remove the GitHub Actions checks it replaces and delete `ci.yml`.

`review` is opt-in the same way, with **No machine** (`--no-machine`).

## Requiring the check

In the repository's branch ruleset: **Settings → Rules → Rulesets → Require
status checks to pass → Add checks**, name `<project>/ci`, and pick the
Reliant App as its source. Pinning the source matters: a check required from
the App (`integration_id` 1041943 for Reliant's hosted App; a self-hosted
deployment's App has its own id, `gh api /apps/<slug> --jq .id`) cannot be
satisfied by an Actions job or a commit status posted under the same name.

```json
"required_status_checks": [{"context": "<project>/ci", "integration_id": 1041943}]
```

With a merge queue, both workflows also post on every group
(`check_suite.requested` on `gh-readonly-queue/...`), so a required check
never stalls the queue. `review` passes a group without a second review: the
queue admitted only pull requests whose review passed.

## Forks and untrusted authors

A run executes the pull request's code on your machine, so only trusted heads
run: not a draft, the head branch in this repository (`head.repo.full_name`;
a fork's never runs), and an author who is an OWNER, MEMBER or COLLABORATOR,
or whose GitHub user id is in the workflow's `trusted_ids` input (add
dependabot[bot], 49699333, to gate its PRs). Anything else is filtered out
before a run exists, so a required check stays pending: a maintainer who has
read the change pushes the branch into the repository, and it runs from there.

Inside a run, the `fetch` step holds the machine's git credentials and runs
git only: it fetches the commit and its base and writes them as a bundle to a
directory of the run's own, `mktemp -d`'s `<cache>/reliant-ci/run.XXXXXXXX/`
(mode 0700, under a root it refuses unless it is a real directory this user
owns with mode 0700). The `gate` step runs the code with `credentials: none`
(no tokens, credential files hidden on Linux), starting in that directory, in
a repository it makes from the bundle in its private `/tmp`, with git hooks
off: nothing in it points at the project's checkout or its object store.
Caches the sandbox makes private start cold, so a run is slower than
`task ci` on a warm checkout.

## Adding checks of your own

- **A deterministic check** (a linter, a script): add it to the gate under
  `ci.gate.extra_checks` (`ci/extending`). It runs in every tier that runs
  its lane, shows as a row of `<project>/ci`, and its `file:line` output
  becomes annotations. Prefer this to a new workflow.
- **A check that must pass or be re-run on its own** (a slow suite, an
  agent): a new workflow with its own `check: <name>` block, one check per
  workflow. Copy `ci.yaml` for a machine step or `review.yaml` for an agent,
  build `{title, summary, text?, annotations?}` in a `compute` node, route a
  failure to a node with `outcome: failure`, and require it by name like
  `<project>/ci`. The `workflow-builder` skill (in Reliant) has the full
  `check:` reference.
- Edit the workflows freely: they are yours. `reliant workflow validate`
  checks them; `reliant workflow eval` tries a filter or the report mapping
  against a sample event or a gate document.

`ci.workflows: {ci: false}` or `{review: false}` stops forge writing one,
as for the GitHub files.

## Rules

- Never require `<project>/ci` before it has reported on a pull request:
  every open PR blocks until a run posts it.
- A missing check means no run fired: the trigger is not enabled, the event's
  repository is not the activation's `repository`, or the filter refused the
  head (draft, fork, untrusted author). **Workflows → Automations** lists the
  activation's firings and each skip's reason.
- The workflows need a Reliant release with the workflow `check:` block and a
  forge with `forge ci gate --tier` and `--json`.
