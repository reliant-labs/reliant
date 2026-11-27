---
name: migrations/v1.12.0/ci-oidc-uploads
description: CI uploads test runs with GitHub OIDC and no secret (forge v1.12.0). In GitHub Actions, `forge test report --upload` exchanges the job's OIDC token for a short-lived tests:write token when FORGE_CONTROL_PLANE_TOKEN is not set, so the scaffolded ci.yml's test jobs carry `id-token: write` and no secret. An older workflow is the project's own (scaffold-once), so moving the pin changes nothing in it. `forge project upgrade` adds `id-token: write` to each job that runs `forge test report` (`forge fix ci-oidc-permissions`), keeping comments. Then link the Reliant GitHub App to the org, and retire the tests secret once an OIDC upload has been seen. Use when a .github/workflows/*.yml job runs `forge test report` or `task test:report` without `id-token: write`.
version: v1.12.0
automation: fix:ci-oidc-permissions
detection: forge-detect ci-oidc-permissions
---

# Test uploads with GitHub OIDC, no secret (v1.12.0)

From v1.12.0, `forge test report --upload` running in GitHub Actions needs no
secret. When `FORGE_CONTROL_PLANE_TOKEN` is not set, forge asks GitHub for the
job's OIDC token and exchanges it with the control plane for a `tests:write`
token that lasts minutes. The control plane accepts it only for a repository
covered by a Reliant GitHub App installation that an admin of the org has
linked, so a repository outside the org cannot upload into it.

GitHub mints that token only for a job granted `id-token: write`. A project
forge scaffolds from v1.12.0 has it on the two jobs that upload, and on no other:

```yaml
  test:
    # A job-level block replaces the workflow's, so it repeats the workflow's
    # permissions. `id-token: write` lets `forge test report` upload with
    # GitHub OIDC; only a job that uploads gets it.
    permissions:
      contents: read
      id-token: write
    steps:
      # ...
      - name: Report test results
        if: ${{ !cancelled() }}
        run: task test:report -- --upload || echo "forge test report exited $? (the test step above is the verdict)"
```

## What does not change by itself

`.github/workflows/*.yml` are the project's own (scaffold-once). Moving
`forge_version` changes none of them, and nothing breaks: a step that passes
`FORGE_CONTROL_PLANE_TOKEN` keeps uploading with it, because the secret, when
set, wins over OIDC.

## 1. Grant the permission: `forge fix ci-oidc-permissions`

`forge project upgrade --to v1.12.0` (or later) moves the pin and runs it. To
run it on its own:

```bash
forge fix ci-oidc-permissions --dry-run   # what it would change
forge fix ci-oidc-permissions
git diff -- .github/workflows
```

It edits each job of `.github/workflows/*.yml` with a step whose `run:`
script uploads: some command in it (split at newlines, `;`, `&&`, `||` and
`|`) is `forge test report` or `task test:report` without `--upload=false`
(or `--upload false`). Text anywhere else in the file, a comment included,
does not count. A script forge cannot read reliably that names the report
counts as an upload: an unneeded `id-token` costs nothing, a missing one skips
the upload.

| The job has | It gets |
|---|---|
| its own `permissions` mapping | `id-token: write` added to it |
| no `permissions`, under a workflow-level mapping | a job-level block that repeats the workflow's permissions plus `id-token: write`, above `runs-on:` with the scaffold's comment: a job-level block replaces the workflow's, so `contents: read` must be restated |
| no `permissions` anywhere | `contents: read` and `id-token: write`; the run says so, because the job ran with the repository's default token permissions and now has only these two: add any other its steps need |
| `id-token: write` already, its own or inherited, or `write-all` | nothing |
| `id-token` explicitly set to anything else (`none`, `read`), its own or the workflow's | nothing: forge never overrides it. The run names the job, and the playbook stays listed until you set `id-token: write` on it or pass `--upload=false` to its report |

No other job changes. The file is edited in place at the lines the YAML
parser reports, so comments and quoting stay; the result must decode to the
old workflow with only those jobs' permissions changed, or nothing is written.
Re-running it changes nothing.

Left to you, each named by the run with its file and line: a workflow that
does not parse, a flow-style job, a multi-line flow-style `permissions`, and
`read-all` (a job-level block cannot extend it: list the read permissions the
job needs plus `id-token: write`).

## 2. Link the Reliant GitHub App to the org

The exchange proves which org a repository belongs to through the GitHub App:

1. Install the Reliant GitHub App on the GitHub account or organization that
   owns the repository, with the repository in its selection.
2. As an admin of the Reliant org, link that installation to the org in the
   org's settings on the web app (Reliant cloud: https://forge.reliantlabs.io).

Until both are done, the exchange is refused with the remedy in its message,
and the upload is one line on stderr: the job stays green, as it does with no
credential at all.

## 3. Retire the tests secret

While the report step passes the secret, the secret is what uploads: it
overrides OIDC. So:

1. Stop passing it: delete the `env: FORGE_CONTROL_PLANE_TOKEN` lines from the
   report step, and leave the stored secret where it is.
2. Watch the next run of the job. A green OIDC upload prints
   `forge test report: uploaded N lane run(s)` and the run appears on the
   Tests pages. A refusal prints `upload skipped:` with the control plane's
   remedy (most often: the App is not linked); fix it, or put the env lines
   back.
3. Then delete the secret, if no other workflow reads it:
   `gh secret delete FORGE_CONTROL_PLANE_TOKEN`. A hosted deploy (`release.yml`,
   `deploy.yml`, `build-images.yml`) still needs its `deploy:write` token: keep
   it where those read it.

## Pull requests from forks

GitHub gives a fork's `pull_request` run no OIDC token, so it skips the upload
and stays green, as it did without the secret. One exception: a private
repository with "Send write tokens to workflows from fork pull requests"
enabled gives fork PRs a token, so their runs are recorded under
`refs/pull/N/merge`. That is accepted (the contributor can already read the
repository, and such a token reaches no protected-ref scope); the org can turn
the setting off. forge never uploads from `workflow_run`.

## Opting out

- **Keep the secret:** leave `FORGE_CONTROL_PLANE_TOKEN` on the step; it wins.
- **No OIDC at all:** `FORGE_CI_OIDC=off` on the step, or skip the codemod.
- **No uploads:** `tests.upload: never` in forge.yaml.

More: `forge skill load ci/test-report` (the report step), `forge skill load
migration-upgrade` (how playbooks and codemods run).
