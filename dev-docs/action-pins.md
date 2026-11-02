# Pinned GitHub Actions in privileged workflows

Every third-party action that a privileged job runs is pinned to a full commit
SHA, with its release written as a comment:
`uses: owner/action@<40-hex sha> # vX.Y.Z`. A job counts as privileged when it
gets a write scope: `contents: write`, `id-token: write`, or no permissions
block at all. The workflows covered are `release.yml`, `build-images.yml` and
the `Release gate` job in `ci.yml`. A tag or branch can move. Whoever controls
the action's repository can repoint it, and the next run then executes their
code with the job's token, its OIDC identity and its secrets.

`internal/version/workflow_action_pins_test.go` enforces two things:

- **Immutability.** A privileged job may not use a mutable ref (`@v4`, `@main`,
  a short SHA). Local actions (`./.github/actions/...`) and `docker://` images
  pinned by digest are allowed.
- **The reviewed mapping.** Every pinned commit has to appear in
  `reviewedActionPins`, the action-to-commit table, and its `# vX.Y.Z` comment
  has to match the tag recorded there. Being 40 hex characters proves nothing on
  its own: a mistyped commit, or a fork's commit that is reachable through the
  upstream repository, is just as immutable as the correct one.

## Changing a pin

1. Resolve the release tag to its commit upstream. For a lightweight tag:
   `git ls-remote https://github.com/<owner>/<action> refs/tags/vX.Y.Z`. For an
   annotated tag, take the `^{}` line: that is the commit, not the tag object.
2. Update the `uses:` line, then add or replace the matching row in
   `reviewedActionPins`. Both changes go in the same PR, so the reviewer sees the
   tag-to-commit resolution.
3. `task test:short -- ./internal/version/...`.

## Live verification after a pin change

The static test proves only which code a job will run. It cannot prove that the
pinned commits still authenticate and publish against the real external
services. After a merge that changes a pin in these workflows, the first runs
below are the verification. Until each one has passed, treat the change as
unverified.

**Image build: GCP Workload Identity Federation.** Check the first
`Build & Push Image` run after the merge. It runs on the next release tag, or
dispatch it on `main` with an `image_tag` such as `pin-check-<sha>` if you need
the answer sooner. Pass criteria:

- `Authenticate to GCP (nonprod)` succeeds and logs the nonprod service account
  (`WIF_SA_NONPROD`). `Configure Docker for Artifact Registry` and
  `Build and push` succeed, and the image digest appears in the nonprod GAR.
- `Authenticate to GCP (prod)` succeeds with `WIF_SA_PROD`, and
  `Mirror digest to prod GAR` puts the same digest in the prod GAR.

**Desktop release: signing and publishing.** Before relying on a real tag,
dispatch `Release Electron App` with `dry_run: true`. This builds and signs but
never publishes (`--publish never`). Then check the first real tag run. Pass
criteria:

- `prepare` and all six `build-backend` matrix legs succeed. Their artifacts
  upload and download, which exercises upload-artifact and download-artifact.
- `release-macos`: `Build and Publish macOS apps (Signed & Notarized)`
  succeeds, meaning signing with `CSC_LINK` and notarization both passed.
  `release-linux` succeeds. `release-windows` succeeds, which means Azure
  Trusted Signing signed the installers.
- On a real tag, `finalize` succeeds and `Verify published release` passes:
  `scripts/verify-release-artifacts.sh` finds every variant and every update
  feed live, and the GitHub Release exists.

**Release gate record (`ci.yml`).** The first push to `main` after the merge
writes the `refs/notes/release-gate` note in the `Release gate` job. That
exercises the pinned checkout and download-artifact in a `contents: write`
job.

**If a run fails.** Fix forward; never re-run until green and never unpin to a
tag.

1. Read the failing step. A failure in an authentication or action step
   (`google-github-actions/auth`, `setup-gcloud`, docker/*, or `actions/*`)
   points to the pin. A failure in the project's own build scripts points
   elsewhere.
2. If the pin is the cause, re-resolve the intended tag (see "Changing a pin")
   and compare the result with `reviewedActionPins`. If the commit was wrong,
   correct it. If the release is incompatible with the job, move to the commit
   of a known-good release. Either way, fix the table and the `uses:` line
   together in one PR labelled `release:asap`.
3. Re-run the workflow on the fix: dispatch `build-images.yml`, or the release
   with `dry_run: true` first. Cut the release again only after that run passes
   the checks above. While the image build or desktop release is red, it blocks
   the release it was building. Nothing already published is touched, because a
   failed run publishes nothing.
