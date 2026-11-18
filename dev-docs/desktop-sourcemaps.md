# Desktop renderer source maps

The desktop app's renderer (`frontends/web/dist`, built by `.github/workflows/release.yml`) reports its
browser errors into tenant zero's self-managed prod env as frontend `desktop`. The platform symbolicates an
error only when the maps of that build are in the registry. When they are missing, the culprit is
`app://bundle/assets/main-<hash>.js`. The content hash changes with every build, so every desktop release
re-opened a copy of every browser issue, each with its own `NewIssue` alert (reliant#2535).

## What runs

- **`prepare`.** On a release tag, the job uploads the built renderer as `web-dist-sourcemaps`. On every
  release build (tag or prebuild), it then deletes the `*.map` files from `frontends/web/dist`, so the
  installers ship without them.
- **`sourcemaps`.** This job runs on release tags only. It downloads that artifact and runs:

  ```sh
  task release:sourcemaps DIST=frontends/web/dist BUILD_ID=<version without the v>
  ```

  The task wraps `forge ui sourcemaps push <dist> --frontend desktop --build-id <id> --env desktop --project
  control-plane --org <tenant zero>`.
  - `--env desktop` (`deploy/kcl/desktop/main.k`) deploys nothing. It names the control plane and the credential's variable.
  - The org and project are the env the renderer reports into.
  - The build id is `frontends/web/dist/version.json`'s version. The renderer reports the same value as `service.version`.
- **Credential.** The job reads `FORGE_CONTROL_PLANE_TOKEN` from the `release` environment's
  `RELIANT_SOURCEMAPS_TOKEN` secret.
- **Missing credential.** Without the secret, the job warns and files nothing.
- **Failures.** The job is `continue-on-error`. A failure leaves that release's renderer errors unsymbolicated, which is the state
  before reliant#2535, and never fails a release that otherwise shipped.

The repository variables `RELIANT_SOURCEMAPS_ORG` and `RELIANT_SOURCEMAPS_PROJECT` override the org and project.
The defaults are in `Taskfile.yml`'s `release:sourcemaps`.

## Setting it up (owner, once)

The credential is an **organization** token whose only scope is `sourcemaps:write`. It is registry-only:

- control-plane refuses it on every RPC;
- the registry grants it push and pull on the org's `*-sourcemaps` repositories and nothing else (control-plane
  `internal/registryauth/claims.go`).

It must be minted in the web app: control-plane refuses token administration to a CLI login.

1. Sign in to <https://forge.reliantlabs.io/settings/tokens> as an admin of tenant zero's organization
   (`55949942-3ee9-4cfc-bb7c-817bdba75186`, control-plane `deploy/kcl/prod/config.k`
   `observability.tenant_zero_org_id`). Switch to that org if it is not the active one.
2. Create a token:
   - **Name:** `reliant-desktop-sourcemaps`
   - **Scope:** **only** `sourcemaps:write` (picking it clears every other scope)
   - **Expiry:** 1 year

   Copy the secret. It is shown once.
3. In GitHub, create the environment and restrict it to release tags:
   - reliant-labs/reliant → Settings → Environments → **New environment** `release`. If a release
     ran first, the environment already exists.
   - Deployment branches and tags: **Selected branches and tags** → add the tag rule `v*`.
   - Add no required reviewers. The job runs unattended, after the tag push.
4. Store the secret on that environment, not on the repository:

   ```sh
   gh secret set RELIANT_SOURCEMAPS_TOKEN --repo reliant-labs/reliant --env release
   # paste the secret at the prompt
   ```

5. If the env that the desktop renderer's errors land in is not control-plane's forge project, set the
   repository variable:

   ```sh
   gh variable set RELIANT_SOURCEMAPS_PROJECT --repo reliant-labs/reliant --body <project>
   ```

   The enrichment worker looks maps up under `ProjectOf(org, env)` of that error
   (control-plane `internal/workers/issues/enricher.go`).

## Checking it

After the next release tag:

- The run's `File renderer source maps` job ends with a `[sourcemaps] desktop: N source map(s), build id
  <version>, env desktop: the platform registry` line and a filed record. It does not end with the
  missing-secret warning.
- A desktop browser issue first seen on that release has `src/...` culprits (`reliant forge obs issues prod` and
  `reliant forge obs issue <id> prod` in control-plane). Its fingerprint no longer forks per release.

If the job reports that the registry refused the push, check the token's scope and org first. If maps are
filed but issues stay minified, the project or org is wrong: compare it with where the hosted `reliant-web`
maps are filed (control-plane's release records `reliant-web.sourcemaps`).

## Rotating or revoking

Revoke the token in Settings → API tokens (org tokens), mint a new one as above, and replace the secret with
`gh secret set ... --env release`. Nothing else holds it.
