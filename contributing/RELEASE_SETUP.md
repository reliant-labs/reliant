# Release Setup

## ⛔ Releases are LOCAL-ONLY. CI never builds, publishes or deploys.

Every build, signing, upload, tag and deploy of a release artifact happens on a
maintainer's machine, through this repo's scripts. **CI runs checks only** —
tests, lint, migration checks, vuln scans, the tag-ancestry guard.

`.github/workflows/release.yml` (Electron → Cloudflare R2, the GitHub Release,
the Homebrew dispatch) and `.github/workflows/build-images.yml` (the service
image → GAR) are **deleted** — not disabled, and deliberately without a
`workflow_dispatch` escape hatch. Pushing a tag builds nothing.

Publishing is `./scripts/release-artifacts.sh`; see "step 3" below.

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

### A release is three steps

Those commands are **step 1 of 3**. They open a version-bump PR and create *no
tag*. Once that PR has merged:

```bash
make release-tag              # step 2: tags the merged commit on main
make release-tag-dry-run      # preview which commit would be tagged; creates nothing
```

The tag starts nothing. Step 3 publishes, and each target is separate because
the credentials are:

```bash
make release-artifacts-desktop   # signed+notarized app -> R2. RUN ON EACH PLATFORM.
make release-artifacts-image     # multi-arch service image -> prod GAR
make release-artifacts-github    # the GitHub Release page
make release-artifacts-homebrew  # nudge the Homebrew tap
```

`desktop` does not cross-compile and must be run once on each platform you are
shipping: macOS needs a Mac holding the Developer ID certificate plus an App
Store Connect key to notarize, Windows needs the Azure Trusted Signing service
principal. Shipping a subset is legitimate — the R2 channel feeds encode
per-platform availability — but say so in the release notes.

`DRY_RUN=1` builds everything and publishes nothing. See the header of
`scripts/release-artifacts.sh` for the full credential list.

Step 1 (`./scripts/release.sh`):
1. Validates `docs/data/releases/vX.Y.Z.yaml` and regenerates `docs/changelog.mdx`
2. Updates `electron/package.json` (+ lockfile) on a `release-*` branch
3. Opens a PR against `main`

Step 2 (`./scripts/release-tag.sh`):
1. Finds the commit **on `origin/main`** that introduced this version — the bump
   itself, not main's tip, so work merged after the bump is not swept in
2. Verifies that commit is an ancestor of `origin/main`, and refuses otherwise
3. Creates and pushes the tag. It triggers nothing — see step 3 above.

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
   `task release:notes VERSION=vX.Y.Z` there fetches the version's YAML from
   this repo at send time. It defaults to a dry run, and it additionally
   requires `RELEASE_NOTES_EMAIL_ENABLED=true` in the operator's environment
   before it will send anything. It is currently unset, so no release mail
   goes out.

Nothing about authoring changes: write the YAML here as before.

### PR Labels for Changelog

| Label | Description |
|-------|-------------|
| `changelog:feature` | New functionality |
| `changelog:fix` | Bug fixes |
| `changelog:improvement` | Refactors, UX polish |
| `changelog:breaking` | Breaking changes |
| `changelog:skip` | Internal changes, don't include |

## Credentials

Release credentials are **not** in GitHub Actions secrets any more — nothing in
CI publishes, so nothing in CI needs them. They live in the environment of the
person running step 3:

| Phase | Needs |
|---|---|
| `desktop` (all platforms) | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `R2_ENDPOINT` |
| `desktop` (macOS) | `CSC_LINK`, `CSC_KEY_PASSWORD`, `APPLE_TEAM_ID`, and to notarize either `APPLE_API_KEY`/`_KEY_ID`/`_ISSUER` (preferred) or `APPLE_ID` + `APPLE_APP_SPECIFIC_PASSWORD` |
| `desktop` (Windows) | `AZURE_TENANT_ID`, `AZURE_CLIENT_ID`, `AZURE_CLIENT_SECRET`, `AZURE_TRUSTED_SIGNING_*` |
| `image` | a `gcloud` session with push access to prod GAR |
| `github` | `gh`, authenticated |
| `homebrew` | `HOMEBREW_TAP_TOKEN` |

Prefer the App Store Connect API key over the Apple ID for notarization: a key
belongs to the **team**, an Apple ID to a **person**. v1.7.14 shipped Windows
and Linux but not macOS when the account holder changed — signing kept working
(certificate) while notarization began returning "a required agreement is
missing or has expired" (account identity).

Optional: `SENTRY_DSN`, `STATSIG_CLIENT_KEY`. Absent is fine and leaves
telemetry inert in the build.

## Troubleshooting

### Nothing built after I pushed the tag
That is correct. Releases are local-only and a tag triggers nothing — run step
3 (`make release-artifacts-*`).

### A platform is missing from the release
`desktop` builds for the host platform only. Run it on each platform you ship.
If one cannot be signed, shipping the rest is fine; the R2 channel feeds encode
per-platform availability, so the auto-updater degrades rather than breaks.

### The published app talks to the wrong endpoints
Endpoints come from `electron/release.config.json`, generated from
control-plane's KCL, expanded by `electron/scripts/with-release-config.mjs`.
Never hardcode a URL in a build script — a second declaration drifts silently,
which is exactly how v1.7.5 shipped with no control-plane URL.
- Check CloudFlare Worker is deployed for latest-URL redirects
- Verify `latest-mac.yml` / `latest-linux.yml` exist in R2

## License Files

The following license files are included in distributions:
- `LICENSE` - Reliant Beta License (free until January 1, 2027)
