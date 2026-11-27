---
name: ci/test-report
description: The Report test results step in the scaffolded ci.yml — uploading each test lane and its coverage to the project's control plane, GitHub OIDC (id-token: write, no secret, the Reliant GitHub App linked to the org), the FORGE_CONTROL_PLANE_TOKEN secret that overrides it, fork pull requests, and why the step never fails the job.
---

# Report test results

`task test` and `task test:integration` record every lane they run, with its
coverage, under `.forge/tests/runs/` (gitignored). The scaffolded `ci.yml`'s
`test` and `integration-test` jobs each end with a step that sends that record
to the project's control plane:

```yaml
test:
  permissions:
    contents: read
    id-token: write
  steps:
    # ...
    - name: Report test results
      if: ${{ !cancelled() }}
      run: task test:report -- --upload || echo "forge test report exited $? (the test step above is the verdict)"
```

## No secret: GitHub OIDC

The step needs no secret. The job's `id-token: write` lets GitHub mint an OIDC
token for the run, and forge exchanges it with the control plane for a
`tests:write` token that lasts minutes. A job-level `permissions` block replaces
the workflow's, so it repeats `contents: read`; no other job gets `id-token`.

The control plane accepts the exchange only for a repository it can tie to your
org: the Reliant GitHub App must be installed on the repository's GitHub owner,
with the repository selected, and an org admin must link that installation to
the org in the web app's organization settings (Reliant cloud:
https://forge.reliantlabs.io). Until then the exchange is refused with the
remedy in its message, and the step prints it on one line and stays green.

A fork's pull request gets no OIDC token, so it skips the upload green. One
exception: a private repository with "Send write tokens to workflows from fork
pull requests" enabled gives fork PRs a token, so their runs are recorded under
`refs/pull/N/merge`. That is accepted; the org can turn the setting off.

A workflow forge scaffolded before v1.12.0 gets the permission with `forge fix
ci-oidc-permissions` (`forge skill load migrations/v1.12.0/ci-oidc-uploads`).

## Or a secret

To upload with a token instead (CI that is not GitHub Actions, or an org that
has not linked the App), store a token with the `tests:write` scope as a
secret and pass it to the step:

```yaml
      env:
        FORGE_CONTROL_PLANE_TOKEN: ${{ secrets.FORGE_CONTROL_PLANE_TOKEN }}
```

The secret, if set, overrides OIDC. A `deploy:write` token also works, so a CI
that already deploys needs no new one. A person signed in to the web app mints
it under Settings → API tokens (Reliant cloud:
https://forge.reliantlabs.io/settings/tokens), then stores it:

```bash
gh secret set FORGE_CONTROL_PLANE_TOKEN
```

With no credential at all the step prints one line (`upload skipped: …`) and
the job stays green. In GitHub Actions the same reason is also a warning on the
run's summary, titled `Test results not uploaded` (`Test results only partly
uploaded` when some parts were left out), whenever an upload that was asked for
(`--upload`, or the CI default) is skipped or fails. The step never decides the
job's result: the test step above it is the verdict.

## Turning it off

- `FORGE_TESTS=off` records nothing for one run.
- `tests.upload: never` in `forge.yaml` turns upload off for the project.
- `FORGE_CI_OIDC=off` on the step stops forge asking GitHub for a token.

Locally, `task test:report` summarises the last recorded run; add `--upload` to
send it from your machine once you are signed in. A project scaffolded before
v1.12.0 gets the capture with `forge project upgrade`.
