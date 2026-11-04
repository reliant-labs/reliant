# Repo-scoped review repair worktrees

A workflow `create_worktree` still creates ONE workspace of the chat's registered
project. `project.path` is only a tool working-directory override, never permission
to pick another project or arbitrary filesystem repository.

For review repair in a multi-repo project:

```yaml
- id: wt
  type: create_worktree
  args:
    name: "repair-{{ inputs.pr_number }}"
    target_repo: control-plane  # exact registered relative path in this project
    base_branch: "{{ nodes.pr.data.head.ref }}"
    fetch_branch: "{{ nodes.pr.data.head.ref }}"
    expected_head: "{{ nodes.pr.data.head.sha }}"
```

`base_branch` applies only to `target_repo` when a target is named. Other nested
repos use their own detected defaults. `base_branches` optionally overrides
individual registered relative paths, e.g. `{forge: develop}`. Unknown/absolute/
traversal paths are refused; the activity maps registered paths to repo IDs for
`workspacecreate.Request.BaseBranches`. A standalone project may identify its root
by its registered repo name. An unset target preserves the normal all-repo base
behavior.

`fetch_branch` requires both a target and an exact `expected_head` SHA. The daemon
validates the branch, fetches that single origin ref through subprocess argv (no
shell interpolation), then compares the fetched commit before creating a checkout.
It refuses a moved head and uses the immutable object expression directly as
the start point, bypassing branch-first resolution (even a branch named exactly
the SHA cannot shadow it). Fetch+reuse is refused. Successful creation also
checks the checkout's actual HEAD against expected_head. Siblings receive neither
fetch fields nor the target's base.

`Finish` does not issue a separate `worktree.force_cleanup`: force is delegated
to the daemon create operation's `git worktree add -B`, only after fetched-head
and base validation. A drift refusal therefore cannot prune/delete an existing
branch or worktree before reporting failure.

## Responder composition

`builtin://autopilot-review-responder` now supplies these fields from the PR. Its
new `max_passes_per_run` bounds this invocation independently of the existing
`max_review_rounds` lifetime summary history. A caller that reserves durable repair
attempts before invoking it uses **max_passes_per_run=1, max_review_rounds=3**.
The control-plane review workflow owns the crash-safe reservation ledger; a crash
consumes an attempt even if the builtin never posts its final summary. This runtime
change alone does not make standalone summary-based counting crash-safe.

The control-plane workflow PR #1392 currently HARD-DISABLES repair. Do not turn it
on merely because this code is merged: deploy this runtime/worker/daemon support,
then land a reviewed wrapper change binding `max_passes_per_run=1` before removing
the hard stop. Old runtimes reject these new create_worktree keys explicitly; they
must not be silently dropped. Shared-account review provenance remains a separate
owner architectural hold; this PR grants no activation or merge permission.

## Regression proofs

- Pure registered-repo mapping/path/fetch validation tests in activity handlers.
- Three-repo `workspacecreate` wiring: target-only base and fetch; sibling defaults.
- Real-git daemon proof: feature branch exists only in target, all three checkouts
  succeed; siblings retain different defaults.
- Real-git fetched-head drift refuses before checkout; stale local branch cannot
  override the fetched expected commit.
- Responder scenarios: one pass stops despite new feedback, a second separate
  invocation remains allowed below lifetime cap, a fourth lifetime invocation is
  refused.
