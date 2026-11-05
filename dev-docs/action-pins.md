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
3. Before fixing forward, find out what the failed run already published. A
   failed run is not all-or-nothing:
   - `release-macos`, `release-linux` and `release-windows` each publish their
     own installers and update feeds to R2 independently, so one platform can
     be live while another failed.
   - `finalize` runs whenever `prepare` succeeded (`if: always()`), even when a
     platform job failed: it purges the CDN cache and creates the GitHub
     Release for the tag regardless.
   - `build-images.yml` can push the image to the nonprod GAR and then fail
     before `Mirror digest to prod GAR`.

   So inspect the existing platform artifacts and update feeds
   (`scripts/verify-release-artifacts.sh <vX.Y.Z>` names what is missing), the
   GitHub Release for the tag, and both GARs for the image tag. Record which
   platforms and registries are live for that version; a version that some
   clients already auto-update to cannot be treated as untouched.
4. Re-run the workflow on the fix: dispatch `build-images.yml`, or the release
   with `dry_run: true` first. The release workflow runs from the tagged
   commit, so a re-run of the failed tag still uses the bad pin: ship the fix
   as a new version that supersedes the partial one, and note in the partial
   version's GitHub Release which platforms it reached. Cut it only after the
   dry run passes the checks above. While the image build or desktop release
   is red, it blocks the release it was building.

## Upstream verification record

Each row of `reviewedActionPins` was resolved against the action's upstream
repository with `git ls-remote https://github.com/<owner>/<action>
refs/tags/<tag> 'refs/tags/<tag>^{}'` (the `^{}` line when the tag is
annotated) on 2026-11-03. Every pinned commit equals the commit its tag points
to upstream:

| Action | Tag | Upstream commit |
| --- | --- | --- |
| actions/checkout | v4.4.0 | `11d5960a326750d5838078e36cf38b85af677262` |
| actions/checkout | v6.1.0 | `d23441a48e516b6c34aea4fa41551a30e30af803` |
| actions/setup-go | v6.5.0 | `924ae3a1cded613372ab5595356fb5720e22ba16` |
| actions/setup-node | v6.5.0 | `249970729cb0ef3589644e2896645e5dc5ba9c38` |
| actions/cache | v4.3.0 | `0057852bfaa89a56745cba8c7296529d2fc39830` |
| actions/upload-artifact | v4.6.2 | `ea165f8d65b6e75b540449e92b4886f43607fa02` |
| actions/download-artifact | v4.3.0 | `d3f86a106a0bac45b974a628896c90dbdf5c8093` |
| pnpm/action-setup | v4.3.0 | `b906affcce14559ad1aafd4ab0e942779e9f58b1` |
| docker/setup-buildx-action | v3.12.0 | `8d2750c68a42422c14e847fe6c8ac0403b4cbd6f` |
| docker/metadata-action | v5.10.0 | `c299e40c65443455700f0fdfc63efafe5b349051` |
| docker/build-push-action | v6.19.2 | `10e90e3645eae34f1e60eeb005ba3a3d33f178e8` |
| google-github-actions/auth | v2.1.13 | `c200f3691d83b41bf9bbd8638997a462592937ed` |
| google-github-actions/setup-gcloud | v2.2.1 | `e427ad8a34f8676edf47cf7d7925499adf3eb74f` |

Re-run that resolution, and update this table, in any PR that changes a row.
