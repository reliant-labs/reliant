---
name: ci/credentials
description: Which credential each scaffolded GitHub workflow uses — one FORGE_CONTROL_PLANE_TOKEN for a hosted env (no registry login, no registry secret), GITHUB_TOKEN or your own for a cluster registry, and no secret at all for the ci.yml test upload (GitHub OIDC with id-token write, the Reliant GitHub App linked to the org). Covers registry 401/DENIED on push and fork pull requests. Use before adding a login step or a secret to a workflow.
---

# Credentials per workflow

## A hosted env needs NO registry credential — one token

Where the env pushes to the PLATFORM registry, there is no registry login step
and no registry secret. forge authenticates it with the same
`FORGE_CONTROL_PLANE_TOKEN` the job already holds for the release ledger, and
`forge env build --push` / `forge env deploy` log themselves in before their
first push. So the scaffolded workflows differ by env, not by project:

| Workflow | Login step | Credential |
|---|---|---|
| `release.yml` (hosted by construction) | none | `FORGE_CONTROL_PLANE_TOKEN` |
| `build-images.yml`, hosted build env   | none for the build; the `vuln-scan` job runs `forge registry login <env>` with no flags, because it PULLS rather than pushing | `FORGE_CONTROL_PLANE_TOKEN` |
| `build-images.yml`, cluster build env | `forge registry login <env> --username … --password-stdin` | `GITHUB_TOKEN` or your own |
| `deploy.yml` (non-hosted envs only) | same as above | `GITHUB_TOKEN` or your own |
| `ci.yml`, the `test` and `integration-test` jobs' Report test results step | none | **none on GitHub**: the job's `id-token: write` and GitHub OIDC, once the Reliant GitHub App is linked to the org (`ci/test-report`) |

Two things follow, and both are worth knowing before editing a workflow:

- **Do not add `--username`/`--password-*` for the platform registry.** forge
  refuses them for our host, so the job fails on its first run. The flags are
  for a registry you chose.
- **One secret, not two.** A hosted pipeline needs `FORGE_CONTROL_PLANE_TOKEN`
  and nothing else for images. Adding a registry secret gives you two things to
  rotate and only one of them in anybody's memory.
- **Tests need no secret.** On GitHub the test upload exchanges the job's OIDC
  token for a short-lived `tests:write` token, so a project that never deploys
  from CI stores no secret at all. It needs the Reliant GitHub App installed on
  the repository and linked to the org by an org admin; until then the upload
  skips green. Setting `FORGE_CONTROL_PLANE_TOKEN` on the step still works, and
  overrides OIDC. Fork pull requests get no OIDC token and skip the upload,
  except in a private repository with "Send write tokens to workflows from fork
  pull requests" enabled: there a fork PR gets a token, and its runs are
  recorded under `refs/pull/N/merge`. That is accepted (the contributor can
  already read the repository, and the token reaches no protected-ref scope);
  the org can turn the setting off.

An env that pushes to the platform registry AND one of your own keeps the login
step for yours alone; forge does its half in the same run.

A job that gets a realm **401 / DENIED** on push is almost never missing a
credential. forge composes the push address from the org the token acts for
(there is no `organization` to declare), so check whether the token expired and
whether it carries `deploy:write` — a push needs both the org's subtree and
write access. forge prints a hint saying so. Do not add a `docker login`.
