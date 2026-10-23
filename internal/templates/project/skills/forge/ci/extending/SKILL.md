---
name: ci/extending
description: How to extend the scaffolded CI — add a check to the gate (ci.gate.extra_checks), add a job that needs its own runner, or add a parallel Tier-2 workflow (release, deploy previews, notifications, coverage upload, extra scanners). Use before adding a job to ci.yml or a new file under .github/workflows/.
---

# Extending CI

## Adding a custom workflow

The Tier-2 path. Drop a new file under `.github/workflows/` with any
name forge doesn't already own (`docs.yml`, `tag-release.yml`,
`slack-notify.yml`, …). Convention: open with the canonical
"yours" banner so future readers see at a glance that the file is
user-owned and forge will not touch it:

```yaml
# yours: scaffolded once, never touched again — forge will not overwrite this file
name: Release
on:
  push:
    tags: ["v*"]
permissions:
  contents: write
jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: GoReleaser
        uses: goreleaser/goreleaser-action@v6
        with:
          version: latest
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

`forge project audit` and `forge project map` will report the file as `user-owned`.
`forge lint --banners` ignores user-owned files (the banner check only
fires on forge templates).

## Extending `ci.yml`

`ci.yml` is scaffold-once: it is yours, `forge generate` never rewrites it, and
there is no forge.yaml key that appends jobs to it (the `ci:` block only feeds
the one-time render). Pick the seam by what you are adding:

- **A check** (a codegen drift check, a custom linter, a policy script): add it to
  the gate with `ci.gate.extra_checks` (`ci/gate`). It then runs in `task ci` and in
  the CI `checks` job, with no workflow edit and no extra runner.
- **A job that needs its own runner** (a service container, another OS): edit
  `ci.yml` and add the job, gated on `needs: changes` like the others, with
  `timeout-minutes`.
- **Anything with its own trigger** (cron, workflow_dispatch, release tags): a
  parallel Tier-2 workflow file.

## Common extensions

- **Release.** Tier-2 `tag-release.yml` triggered on `push: tags: ['v*']`
  using GoReleaser, ko, or a hand-rolled `gh release create`.
  Generated `build-images.yml` covers main-branch image pushes;
  release adds tag-driven artifact + image promotion.
- **Deploy beyond k3d/staging/prod.** The generated `deploy.yml`
  iterates the env directories discovered under `deploy/kcl/<env>/main.k`.
  Add a preview-environment Tier-2 workflow keyed on PR labels or a
  manual-promotion Tier-2 workflow keyed on `workflow_dispatch`.
- **Slack / Discord notifications.** Tier-2 `notify.yml` triggered on
  `workflow_run: completed` of `CI` — keeps notification logic out of
  the generated chain.
- **Codecov / coverage upload.** Set `ci.test.coverage: true` in
  `forge.yaml` to have the `test` job upload a coverage
  artifact, then add a Tier-2 workflow that downloads + uploads to
  Codecov on `workflow_run`.
- **Custom security scanners (Snyk, Semgrep).** Tier-2 workflow
  triggered on schedule + PR; the gate's `vuln-go`/`vuln-npm` rows (and the
  `docker-build` job's Trivy step) cover govulncheck/npm audit/Trivy and are
  opinionated, so add scanners alongside rather than try to extend them.
