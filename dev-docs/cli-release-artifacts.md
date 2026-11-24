# Standalone CLI release assets and the daemon channel manifest

Every reliant release tag publishes the `reliant` CLI on its own, beside the
Electron installers: one archive per platform, `checksums.txt`, a keyless
Sigstore signature over them, and `daemon-channels.json`, a public manifest that
names each channel's current release. A self-run daemon or a standalone server
reads that manifest to learn its upgrade target without our control plane, then
verifies what it downloads. This is phase R0 of forge's
`docs/proposals/self-upgrade.md` (§6). Forge's `pkg/selfupdate` (F1, F2) and
`reliant update` (E2, `dev-docs/self-update.md`) are the consumers;
`internal/cliupdate` pins the signing values below.

Nothing that already shipped changed: the installers, their update feeds
(`latest*.yml`, `alpha*.yml`), their URLs and the Homebrew dispatch are as they
were.

## What a release publishes

| Asset | GitHub Release | downloads.reliantlabs.io |
| --- | --- | --- |
| `reliant_<os>_<arch>.tar.gz` for linux, darwin and windows on amd64 and arm64 | yes | `cli/<tag>/` |
| `checksums.txt` (`<sha256>  <file>`, sha256sum format) | yes | `cli/<tag>/` |
| `daemon-channels.json` as this release wrote it | yes | `cli/<tag>/` |
| `reliant-release.sigstore.json`, the signature | yes | `cli/<tag>/` |
| `daemon-channels.json`, the live manifest | no | bucket root |
| `reliant_<os>_<arch>.tar.gz.cdx.json`, a CycloneDX SBOM of each archive's binary | yes | no |

The SBOMs are not part of the signed set (below). `cli-sbom` writes them with
the pinned forge's `forge vuln sbom --file`, which reads the binary's Go build
info (modules, versions, toolchain) with forge's pinned syft and names the
archive and the tag as the SBOM's subject, as forge's own releases do. They are
evidence for a scanner (`forge vuln scan <file>.cdx.json`, grype, Dependency-Track),
not something a verifier pins: a re-run regenerates and replaces them. After
`cli-publish` succeeds, `sbom-published` downloads and verifies the retained
signed set from the GitHub Release before describing it, rather than using the
rerun's potentially different build artifacts. A missing or corrupt stored set
fails closed. Unpublished dry runs describe their own `cli-archives` artifact.

For example, `https://downloads.reliantlabs.io/cli/v1.9.0/reliant_linux_arm64.tar.gz`
and `https://github.com/reliant-labs/reliant/releases/download/v1.9.0/reliant_linux_arm64.tar.gz`.
The archive names carry no version, as forge's do, so
`/releases/latest/download/<name>` resolves without an API call. Each archive
holds `reliant` (`reliant.exe` on Windows), `LICENSE` and `LICENSES.txt` at its
root. Windows also ships a `.tar.gz`; `tar.exe` has been part of Windows 10
since 1803.

Everything under `cli/<tag>/` is immutable once written. The root
`daemon-channels.json` is the only object that changes, and it is uploaded last,
so a client never reads a manifest whose files are not there yet.

## The binaries are the installers' binaries

Nothing is built twice. `build-backend` builds `reliant-backend` once per target
with the release's ldflags (version, commit, commit time, build defaults), and
those six binaries go into the installers. The `cli-archives` job packages the
same bytes under the CLI's name (`scripts/release-cli.sh package`). The archives
are reproducible: GNU tar with sorted entries, owner 0, every mtime set to the
commit time, and `gzip -n`.

A tag that reuses an Electron prebuild (`ELECTRON_REUSE_PREBUILD`) skips
`build-backend`, so it takes the prebuild run's `cli-archives` artifact instead.
`scripts/release-prebuild.sh find` reuses only a prebuild that has that
artifact.

`.goreleaser.yml` is not used for any of this, and nothing invokes it. It
predates `release.yml`'s build. Its ldflags name a package that no longer exists
(`internal/builddefaults`), and it skips windows/arm64. The release pipeline's
build is `build-backend` in `release.yml`.

## daemon-channels.json (schema 1)

```json
{
  "schema": 1,
  "repository": "reliant-labs/reliant",
  "written_by": {
    "tag": "v1.9.0",
    "commit": "<40-hex sha>",
    "bundle": { "url": "https://downloads.reliantlabs.io/cli/v1.9.0/reliant-release.sigstore.json" }
  },
  "channels": {
    "latest": {
      "version": "1.9.0",
      "tag": "v1.9.0",
      "commit": "<40-hex sha>",
      "commit_time_unix": 1763500000,
      "checksums": { "url": "https://downloads.reliantlabs.io/cli/v1.9.0/checksums.txt", "sha256": "<hex>" },
      "bundle": { "url": "https://downloads.reliantlabs.io/cli/v1.9.0/reliant-release.sigstore.json" },
      "platforms": {
        "darwin/amd64": { "url": "https://downloads.reliantlabs.io/cli/v1.9.0/reliant_darwin_amd64.tar.gz", "sha256": "<hex>" },
        "darwin/arm64": { "url": "…", "sha256": "…" },
        "linux/amd64": { "url": "…", "sha256": "…" },
        "linux/arm64": { "url": "…", "sha256": "…" },
        "windows/amd64": { "url": "…", "sha256": "…" },
        "windows/arm64": { "url": "…", "sha256": "…" }
      }
    },
    "alpha": { "…": "the same shape, for the newest prerelease tag" }
  }
}
```

- **`schema`.** Bump it for any change a schema-1 reader would misread. Adding a
  field is not one. The publisher refuses to overwrite a manifest of another
  schema.
- **Channels.** These follow the installers' channels: a prerelease tag
  (`-rc.N`, `-beta`, `-alpha`) writes `alpha`, and any other tag writes
  `latest`. A release changes only its own channel. It does not touch the other.
  A client on `alpha` should take the newer of `alpha` and `latest` by
  `commit_time_unix`, because an RC train's stable release does not rewrite
  `alpha`.
- **Ordering.** `commit_time_unix` orders releases. It is the committer time of
  the tagged commit, the same build number the binary carries
  (`internal/version.CommitTime`, `scripts/release-identity.sh --commit-time`).
  The publisher never moves a channel to an older commit. Re-running an old
  release fails rather than replace a newer manifest.
- **Platforms** are keyed `GOOS/GOARCH`.

## Signing

One keyless Sigstore attestation (`actions/attest`) covers the six archives,
`checksums.txt` and the release's `daemon-channels.json`. The `cli-publish` job
creates it, and it is the only job in `release.yml` with `id-token: write`. It is
modelled on forge's `release-attest.yml` (forge `docs/releasing.md`, "Signed
release assets"). The values a verifier pins:

| | |
| --- | --- |
| Certificate identity (regex) | `^https://github\.com/reliant-labs/reliant/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$` |
| OIDC issuer | `https://token.actions.githubusercontent.com` |
| Predicate type | `https://github.com/reliant-labs/reliant/attestations/release-assets/v1` |
| Predicate | `{"repository": "reliant-labs/reliant", "release": "<tag>", "commit": "<sha>"}` |
| Bundle asset | `reliant-release.sigstore.json` |

A verifier must also require the predicate's `release` to equal the tag it is
installing, and its `repository` to equal `reliant-labs/reliant`. Without that
check, an older signed set replayed under a newer tag would pass.
`tools/test-affected/release_cli_workflow_test.go` holds `release.yml` equal to
this table. Change both together, and every verifier with them.

To verify an archive:

```sh
gh attestation verify reliant_linux_amd64.tar.gz --bundle reliant-release.sigstore.json \
  --repo reliant-labs/reliant \
  --cert-identity-regex '^https://github\.com/reliant-labs/reliant/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$' \
  --cert-oidc-issuer https://token.actions.githubusercontent.com \
  --predicate-type https://github.com/reliant-labs/reliant/attestations/release-assets/v1 \
  --deny-self-hosted-runners --format json \
  | jq -e '.[0].verificationResult.statement.predicate.release == "v1.9.0"'

# or, with cosign (add --new-bundle-format on cosign 2.x):
cosign verify-blob-attestation --bundle reliant-release.sigstore.json \
  --certificate-identity-regexp '<the identity regex above>' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --type https://github.com/reliant-labs/reliant/attestations/release-assets/v1 \
  reliant_linux_amd64.tar.gz
```

To verify the live manifest, fetch `written_by.bundle.url` and verify
`daemon-channels.json` against it the same way. Its predicate's `release` must
equal `written_by.tag`. The bundle is immutable and is named by the manifest
itself, so a publish in progress cannot leave the manifest and its signature
mismatched. Then check each archive's sha256 against the manifest, or against
that release's verified `checksums.txt`. Never trust a `checksums.txt` that has
not been verified.

**What it proves.** The certificate names `release.yml` running at a release
tag. Unlike forge's, the archives are built in that same workflow run
(`build-backend`), so the signature says that `release.yml` at that tag built and
published these assets. It also stops anyone from changing a signed release's
assets after the fact. A signature is never replaced (see "Recovery" below).

**What it does not prove.** It does not prove what the source at that commit
contains. Someone who can push a release tag gets their release signed too.

## The pipeline

`release.yml`, on a tag push:

1. `build-backend` builds the six binaries. This job is unchanged.
2. `cli-archives` packages and verifies them, then uploads the `cli-archives`
   artifact (kept 90 days, like the platform builds). When the tag reuses a
   prebuild, it takes the prebuild's archives instead. Prebuild and dry-run
   dispatches run this job too. It publishes nothing.
3. `finalize` creates the GitHub Release. This job is unchanged.
4. `cli-publish` (tag only, never a dry run):
   1. recovers an already signed set for the tag (`recover`, below); only when
      there is none does it go on to the next two steps;
   2. reads the published manifest from R2 (`fetch-manifest`; "none yet" is not
      an error, but an unreadable manifest is) and writes the new manifest
      (`manifest`) and the predicate;
   3. signs;
   4. checks the bundle exactly as a verifier will (always, recovered sets too);
   5. stores the whole signed set on the GitHub Release (`store-github`), the
      signature last;
   6. stores it under `cli/<tag>/` on R2 (`store-r2`), the signature last;
   7. only then uploads the root `daemon-channels.json` (`publish-root`), which
      refuses unless both stores hold this exact set, and refuses to move any
      channel backwards;
   8. purges `daemon-channels.json` from the CDN.
5. `verify-published` reads the manifest through the CDN until it names the tag
   for its channel. It then downloads every archive and `checksums.txt` from R2
   (through the CDN) and from the GitHub Release, and requires each copy's bytes
   to hash to the manifest's digest, which must itself be `checksums.txt`'s. The
   signature must be the same bytes in both places. A 200 alone proves nothing.
   It waits for `cli-publish`.
6. `cli-sbom` describes each archive's binary (`scripts/release-cli.sh sbom`)
   and keeps the SBOMs as the `cli-sboms` artifact; when `cli-publish`
   succeeded, it attaches them to the GitHub Release (`store-sbom-github`, which
   refuses a Release with no signed set). Warn-first: it is
   `continue-on-error` and nothing needs it, so an SBOM forge cannot write is a
   warning on the run, never a failed release. Prebuild and dry-run dispatches
   run it too and attach nothing. `TestReleaseCLISBOMJob` holds those
   conditions.

### Recovery: a tag is signed once

The signed set of a tag is its six archives, `checksums.txt`, the tag's
`daemon-channels.json` and `reliant-release.sigstore.json`. Each store gets the
signature last, so a store that holds the signature holds the whole set.

- **Recover, never re-sign.** If the GitHub Release or `cli/<tag>/` on R2 holds
  the signature, `recover` takes that set (GitHub first) and the job stores it
  again unchanged. The manifest is not regenerated: if another channel moved
  meanwhile, a regenerated manifest would differ from the one clients may
  already hold.
- **Never overwrite.** A store that already holds a signed set must hold exactly
  this one, byte for byte (a no-op); anything else fails and changes nothing.
  Unsigned leftovers of an interrupted upload are replaceable.
- **Durable before visible.** The root manifest changes only after both stores
  hold the set.
- **Never backwards.** If a newer release published the root manifest while this
  one was interrupted, the recovered manifest lacks that release's entry, so
  `publish-root` refuses: the tag's signed set is complete in both stores, and
  its channel moves forward with that channel's next release.

`scripts/test-release-cli.sh` replays exactly that: a set reaches R2, its GitHub
attachment fails, another channel's release publishes, then the re-run recovers
the original set from R2, attaches it unchanged, and leaves the root manifest
alone.

The `releases` concurrency group serializes release runs, so the manifest's
read-modify-write cannot race another release.

`scripts/release-cli.sh` makes every one of these decisions, and
`scripts/test-release-cli.sh` proves them offline with a fake `aws` (R2), a
fake `gh` (the GitHub Release) and a fake `curl` (the CDN and github.com). `TestReleaseCLIScript` runs that test in the release gate.
`TestReleaseCLIJobs` and `TestReleaseCLISigning`
(`tools/test-affected/release_cli_workflow_test.go`) evaluate the job conditions
and hold the signing contract.

## First release after this lands

None of this can run outside a real tag, so the first tag is the live check.
Pass criteria:

- `cli-archives` succeeds, and so does `cli-publish`. In `cli-publish`, "Check
  the signature as a verifier will" verifies all eight subjects.
- The GitHub Release lists six `reliant_*.tar.gz`, `checksums.txt`,
  `daemon-channels.json` and `reliant-release.sigstore.json`.
- `curl -s https://downloads.reliantlabs.io/daemon-channels.json | jq .` names
  the tag for its channel.
- The `verify-published` step "The CLI archives and the channel manifest are
  live" passes.
- `cli-sbom` (since the CLI SBOMs landed) is green and the GitHub Release lists
  six `reliant_*.tar.gz.cdx.json`. `gh release download <tag> -p
  'reliant_linux_amd64.tar.gz.cdx.json' -O - | jq '.metadata.component'` names
  the archive and the tag. A yellow `cli-sbom` is not a failed release: read its
  `::warning::` lines.

If `cli-publish` fails, re-run it. With no signature stored anywhere it signs
again; otherwise it recovers the stored set and finishes storing it. Fix
forward. Never delete a signature to get a release signed again. A deletion
shows in Rekor.
