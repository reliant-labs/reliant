---
name: ci/test-report
description: The Report test results step in the scaffolded ci.yml — uploading each test lane and its coverage to the project's control plane, the FORGE_CONTROL_PLANE_TOKEN secret and where to mint it, and why the step never fails the job.
---

# Report test results

`task test` and `task test:integration` record every lane they run, with its
coverage, under `.forge/tests/runs/` (gitignored). The scaffolded `ci.yml`'s
`test` and `integration-test` jobs each end with a step that sends that record
to the project's control plane:

```yaml
- name: Report test results
  if: ${{ !cancelled() }}
  env:
    FORGE_CONTROL_PLANE_TOKEN: ${{ secrets.FORGE_CONTROL_PLANE_TOKEN }}
  run: task test:report -- --upload || echo "forge test report exited $? (the test step above is the verdict)"
```

## The secret

The upload needs a token with the `tests:write` scope; a `deploy:write` token
also works, so a CI that already deploys needs no new one. A person signed in to
the web app mints it under Settings → API tokens (Reliant cloud:
https://forge.reliantlabs.io/settings/tokens), and stores it as the repository
secret:

```bash
gh secret set FORGE_CONTROL_PLANE_TOKEN
```

Without the secret the step prints one line (`upload skipped: not signed in
to …`) and the job stays green. The step never decides the job's result: the
test step above it is the verdict.

## Turning it off

- `FORGE_TESTS=off` records nothing for one run.
- `tests.upload: never` in `forge.yaml` turns upload off for the project.

Locally, `task test:report` summarises the last recorded run; add `--upload` to
send it from your machine once you are signed in. A project scaffolded before
v1.12.0 gets the capture with `forge project upgrade`.
