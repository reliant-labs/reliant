# Release Setup

## Releases

Electron app releases are built and published by the [control-plane](https://github.com/reliant-labs/control-plane) CI pipeline. When a new version tag is detected on GHCR, the control plane:

1. Builds the Electron app for macOS, Windows, and Linux
2. Creates a GitHub Release on this repo with the artifacts
3. Updates the Homebrew tap
4. Uploads to downloads.reliantlabs.io

To trigger a release manually, use the `release-electron` workflow dispatch in the control-plane repo.

## Quick Release Commands

```bash
# Release candidate (prerelease)
make release-rc           # 0.2.3 → 0.2.4-rc1 (first RC)
                          # 0.2.4-rc1 → 0.2.4-rc2 (next RC)

# Patch release (removes RC suffix or increments patch)
make release-patch        # 0.2.4-rc2 → 0.2.4 (stable from RC)
                          # 0.2.4 → 0.2.5 (next patch)

# Minor release
make release-minor        # 0.2.4 → 0.3.0

# Major release
make release-major        # 0.3.0 → 1.0.0
```

### A release is two steps

Those commands are **step 1 of 2**. They open a version-bump PR and create *no
tag*. Once that PR has merged:

```bash
make release-tag              # tags the merged commit on main, starts the builds
make release-tag-dry-run      # preview which commit would be tagged; creates nothing
```

Step 1 (`./scripts/release.sh`):
1. Validates `docs/data/releases/vX.Y.Z.yaml` and regenerates `docs/changelog.mdx`
2. Updates `electron/package.json` (+ lockfile) on a `release-*` branch
3. Opens a PR against `main`

Step 2 (`./scripts/release-tag.sh`):
1. Finds the commit **on `origin/main`** that introduced this version — the bump
   itself, not main's tip, so work merged after the bump is not swept in
2. Verifies that commit is an ancestor of `origin/main`, and refuses otherwise
3. Creates and pushes the tag, which triggers `Release Electron App` and
   `Build & Push Image`

#### Why it is split

`main` squash-merges — there is not one merge commit in its history. A tag
created on the `release-*` branch therefore points at a commit that main never
takes: the squash produces a new object and the tag stays behind on the branch
forever.

This is not hypothetical. Every tag from **v1.7.8 through v1.7.12** is off
main, as are fifteen older ones going back to v1.2.3-rc1, and
`git describe --tags --abbrev=0 origin/main` still answers **v1.7.7**.

Nothing warned, because from the release's point of view everything worked —
the tag existed, the artifacts built, the app shipped. The damage is only
visible to things that read history from tags, and those fail quietly too. The
one that forced the fix: `go get` derives a pseudo-version from the highest tag
*reachable* from the pinned commit, so pinning reliant's `main` yields
`v1.7.8-0.<date>-<sha>`, which sorts **below** the released `v1.7.12`. Pinning
main is then a content upgrade that is a version-number *downgrade*, and Go's
minimum-version selection can silently undo it.

Tagging after the merge is the only arrangement that is correct by
construction: tagging a merge commit needs merge commits (main has none), and
committing the bump straight to main gives up review of the release notes.

#### The guard

```bash
make check-release-tags       # every release tag must be an ancestor of main
```

This also runs in CI on every tag push (`.github/workflows/release-tag-guard.yml`),
so a tag made from the GitHub UI or by hand — which never runs the scripts — is
caught too.

Tags at or below `BASELINE_TAG` (currently `v1.7.12`) are exempt: they are
already published, and the Electron artifacts on R2 and the Homebrew cask
reference them by name. **Do not move or delete a published tag** — it is the
same class of error as re-cutting a burned Go module version. If a new tag
lands off main, cut the next version through the two-phase flow rather than
raising the baseline.

## Changelog

Before creating a published stable release tag, update the changelog:

```bash
# See PRs since last release, grouped by label
make changelog

# Generate a draft YAML entry for the next release
make changelog-draft VERSION=vX.Y.Z
```

See [docs/CHANGELOG_GUIDE.md](docs/CHANGELOG_GUIDE.md) for full changelog documentation.

The YAML files under `docs/data/releases/` are the source of truth for two
separate consumers:

1. `make generate-changelog` renders them into `docs/changelog.mdx`, which
   Mintlify publishes at <https://docs.reliantlabs.io/changelog>.
2. The **release-notes email**, which is sent from the control-plane repo —
   `.github/workflows/release-notes.yml` there fetches the version's YAML from
   this repo at send time. It is a manual `workflow_dispatch`, it defaults to a
   dry run, and it additionally requires the repo variable
   `RELEASE_NOTES_EMAIL_ENABLED=true` in control-plane before it will send
   anything. That variable is currently unset, so no release mail goes out.

Nothing about authoring changes: write the YAML here as before.

### PR Labels for Changelog

| Label | Description |
|-------|-------------|
| `changelog:feature` | New functionality |
| `changelog:fix` | Bug fixes |
| `changelog:improvement` | Refactors, UX polish |
| `changelog:breaking` | Breaking changes |
| `changelog:skip` | Internal changes, don't include |

## Secrets

All release secrets (Apple code signing, Azure Trusted Signing, Cloudflare R2, Homebrew, Customer.io, etc.) are managed in the control-plane repo's GitHub Actions secrets. See the control-plane repo for details.

## Troubleshooting

### Release Not Triggered
- Check the control-plane's `watch-reliant-image` workflow — it polls GHCR every 5 minutes
- Verify the tag was pushed and the GHCR image was built
- Manually dispatch `release-electron` in the control-plane repo with the desired `reliant_ref`

### Download URLs / Auto-Updater / Code Signing Issues
- See the control-plane repo's release workflow for configuration details
- Check CloudFlare Worker is deployed for latest-URL redirects
- Verify `latest-mac.yml` / `latest-linux.yml` exist in R2

## License Files

The following license files are included in distributions:
- `LICENSE` - Reliant Beta License (free until January 1, 2027)
