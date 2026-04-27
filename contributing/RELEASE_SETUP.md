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

These commands use `./scripts/release.sh` to:
1. Update `electron/package.json` version
2. Create a git tag (e.g., `v0.0.1`)
3. Push the tag — the control-plane's `watch-reliant-image` workflow detects the new tag and triggers the release

## Changelog

Before creating a published stable release tag, update the changelog:

```bash
# See PRs since last release, grouped by label
make changelog

# Generate a draft YAML entry for the next release
make changelog-draft VERSION=vX.Y.Z
```

See [docs/CHANGELOG_GUIDE.md](docs/CHANGELOG_GUIDE.md) for full changelog documentation.

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
