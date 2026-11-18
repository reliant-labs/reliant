# Workflow checks (`check:`): post-release verification and recovery

A workflow's `check:` block makes the engine post a GitHub check run on the
event's head commit, as the Reliant GitHub App, for every firing
(`internal/workflowcheck`, reliant#2885). reliant#2863 adds one more writer:
a firing **refused before launch** (the trigger's machine was deleted, the
declaration did not resolve, the event did not fit a declared input) now
opens its check too (`internal/triggers/check.go`, `openRefusedCheck`), and
the syncer concludes it `failure`. Before it, such a firing posted no check,
so a required check stayed missing.

These are writes to customers' repositories that a rollback does not undo,
so run the check below after every release that carries either change.

## How a check moves, and what already concludes a stale one

One row per check in `workflow_check_runs` (migration
`20261118022959_workflow_check_runs`). The trigger's fire activity inserts it
(`workflowcheck.Open`); the run stamps its start and end. The worker's
`workflowcheck.Syncer` (every worker runs one, rows leased with a fencing
token) derives what the check should say from the firing and its run
(`derive.go`) and writes the difference to GitHub:

| Firing / run | GitHub check |
|---|---|
| queued for capacity or a machine, starting | `queued`, title `Queued` |
| first node started | `in_progress`, title `Running` |
| completed | `completed` / `success`, `Passed` |
| completed into `outcome: failure` | `completed` / `failure`, `Failed` |
| run failed | `completed` / `failure`, `Failed: the run failed` |
| refused before launch (firing outcome `failed`) | `completed` / `failure`, title `Reliant could not run this check: re-run it`, summary `Reliant could not run this check: <refusal reason>.` |
| skipped (overlap, filter) | `completed` / `cancelled`, `Not run: …` |
| paused longer than 15m (`PauseGrace`) | `completed` / `failure` |

The syncer already concludes stale checks itself, as long as a worker that
has it is running:

- **Timeout.** A check not concluded by its `timeout` (default 6h, 1m to 72h,
  from the firing) concludes `timed_out` (`derive`).
- **Dead run.** A run that died without reporting is concluded from its
  workflow row; a deleted firing or run concludes `failure`.
- **Writes that keep failing** are retried with backoff (up to 5m). The
  syncer gives up `timeout + 1h` after the check was created, or at once when
  GitHub rejects the write or the activation may no longer write it. A
  given-up row has `finished_at` set and `github_status` not `completed`: it
  **can stay queued or in progress on GitHub**. Recovery (below) closes those.

## 1. Verify after deploy

Do this once the worker release is live, on a test repository where the
Reliant GitHub App is installed and that you can push to.

**Set up** (once): a project on that repository with three workflows, each
with a GitHub trigger on `pull_request` (`opened`, `synchronize`), and each
declaring a short check so a stuck one shows up quickly:

```yaml
check: {name: reliant/smoke-pass, timeout: 10m}   # one node that succeeds
check: {name: reliant/smoke-fail, timeout: 10m}   # a node that ends with outcome: failure
check: {name: reliant/smoke-refused, timeout: 10m} # a required input the event cannot fill
```

For the refused case, declare an input that the trigger maps from a field a
`pull_request` event does not carry, so the fire refuses with "the event did
not fit a declared input". Alternatively point its trigger at a machine and
delete the machine.

**Fire** all three: open (or push to) a pull request; note its head SHA.

**Read the checks** as GitHub sees them:

```bash
O=<owner> R=<repo> SHA=<head sha>
gh api "repos/$O/$R/commits/$SHA/check-runs" --paginate \
  --jq '.check_runs[] | select(.name | startswith("reliant/smoke")) |
        {id, name, app: .app.slug, status, conclusion, title: .output.title, summary: .output.summary, details_url}'
```

Look for, within a minute or two of each transition:

| Check | Expect |
|---|---|
| `reliant/smoke-pass` | `queued`, then `in_progress` (title `Running`) once the first node starts, then `completed` / `success`, title `Passed` |
| `reliant/smoke-fail` | `completed` / `failure`, title `Failed` |
| `reliant/smoke-refused` | present (it did not exist before #2863), `completed` / `failure`, title `Reliant could not run this check: re-run it`, summary naming the refusal reason |

Every one: `app` is the Reliant App's slug (the same slug for all three), and
the pass and fail checks' `details_url` opens the run's page.
Repeat the `gh api` call while the pass run is live to see the `queued` and
`in_progress` states; re-running it once more after 10 minutes must show no
smoke check still `queued` or `in_progress`.

**Read the engine's side.** From the checkout whose `prod` env runs the
reliant worker (`../control-plane`):

```bash
# every warning the check writer logged (rejected writes, given-up checks,
# lost leases, invalid check blocks, firings that post no check)
reliant forge obs logs prod '"[workflowcheck]"' --severity warn --severity error --since 1h

# the sync passes and the opens, with their errors
reliant forge obs traces prod --route workflowcheck.SyncOnce --since 1h
reliant forge obs traces prod --route workflowcheck.Open --since 1h
```

and the rows, in the reliant database:

```bash
reliant forge db query prod --database reliant --confirm-env prod \
  --reason "post-deploy check: workflow check writes" \
  "SELECT name, owner || '/' || repo AS repo, github_status, github_conclusion,
          attempts, left(last_error, 200) AS last_error, finished_at, created_at
     FROM workflow_check_runs
    WHERE created_at > now() - interval '1 hour'
    ORDER BY created_at DESC LIMIT 50"
```

**Failure criteria.** Stop writes (step 2) if any of these holds:

- a smoke check has the wrong status or conclusion, or never appears;
- a check stays `queued` / `in_progress` after its run ended;
- `[workflowcheck]` warnings show `abandoned a check GitHub rejected` or
  `gave up on a check that could not be written` for more than an isolated
  check, or rows pile up with `attempts > 0`;
- a check is posted on a commit, repository or name other than the firing's.

## 2. Stop further writes

There is **no global switch** that turns check writes off: the `check:`
block is the only declaration, and the syncer runs in every worker. What
stops writes:

- **One workflow** (config only, no release): remove its `check:` block, or
  disable its trigger. New firings open no check. Checks it already opened
  are still concluded by the syncer, so nothing is left hanging.
- **Refused firings opening checks** (#2863's change): roll the workers back
  to the previous release. A release that has `internal/workflowcheck`
  (#2885) but not #2863 stops opening checks for refusals and keeps
  concluding every check already open, the refused ones included (it derives
  `failure` from the failed firing the same way).
- **Every check write** (the syncer itself is wrong): roll the workers back
  to a release before #2885. Nothing then writes checks, and nothing
  concludes the ones already open: run recovery (step 3) right after.

Rolling back never removes a check from GitHub; only a later write changes
it, and only the App that created a check run can update it.

## 3. Recover checks left queued or in progress

While a syncer runs, an unfinished row is concluded by its timeout at the
latest. Recover by hand only the checks no syncer will conclude:

- rows it gave up on (`finished_at` set, GitHub not `completed`), and
- every unfinished row when no syncer runs (rolled back before #2885), or one
  you do not want to wait out to its timeout.

**List them** (age threshold `30 minutes`; adjust):

```bash
reliant forge db query prod --database reliant --confirm-env prod \
  --reason "recover stale workflow checks" \
  "SELECT id, owner, repo, name, github_check_run_id, github_status,
          finished_at IS NOT NULL AS given_up, left(last_error, 200) AS last_error, created_at
     FROM workflow_check_runs
    WHERE github_check_run_id IS NOT NULL
      AND github_status IN ('queued', 'in_progress')
      AND (finished_at IS NOT NULL OR created_at < now() - interval '30 minutes')
    ORDER BY created_at"
```

A row with no `github_check_run_id` was never posted, so there is nothing on
GitHub to close.

**Confirm each on GitHub, then conclude it** as the Reliant App (only the App
that created a check run may update it), `cancelled` with a summary saying why:

```bash
O=<owner> R=<repo> ID=<github_check_run_id>
reliant github gh --repo "$O/$R" -- api "repos/$O/$R/check-runs/$ID" \
  --jq '{name, app: .app.slug, status, conclusion}'
# only if status is still queued or in_progress and app is the Reliant App:
reliant github gh --repo "$O/$R" -- api -X PATCH "repos/$O/$R/check-runs/$ID" \
  -f status=completed -f conclusion=cancelled \
  -f 'output[title]=Cancelled by Reliant' \
  -f 'output[summary]=Reliant stopped this check after a release problem and did not finish it. Re-run the check, or push again, to run it.'
```

(`reliant github token --as app --repo "$O/$R"` gives the same identity for a
plain `GH_TOKEN=… gh api …`.) For a batch, feed the listing's
`owner,repo,github_check_run_id` rows through the same two calls in a loop,
one repository at a time.

If a syncer runs again later (a fixed release) and the row was not finished,
it converges the check on what it derives from the firing and its run, so a
hand-cancelled check may be rewritten with the run's real conclusion. That is
the intended end state; nothing needs to be changed in the table.
