# Proposal: a desktop release target

**Status:** proposal, not implemented. Written for owner review; nothing here
has landed in forge, control-plane or reliant.
**Date:** 2026-10-07
**Scope:** forge (schema, CLI, `pkg/release`), with adoption tasks in
control-plane and retirement tasks in reliant.

## Summary

The Reliant Electron app is the one artifact we ship that does not go through
forge. forge only runs it in dev, as the `reliant-electron` host worker. Every
release is built, signed, notarized, published and announced by reliant's
1,441-line `.github/workflows/release.yml`. That file holds its own copy of the
platform matrix, the channel rule, the feed file names and the CDN purge list,
and it references 29 GitHub secrets. Several of those secrets are not credentials.

This proposal makes a desktop app a declared forge artifact. The recommended
design:

1. **`forge.DesktopApp`** is a new entity, declared once and bound per env with
   a `runtime`, the way a workload or frontend is. It names its source, its
   packager (electron-builder first), the platforms it ships and, for each
   platform, the **builder** that runs it and the **signing identity** it
   uses.
2. **Builders are a primitive.** forge orchestrates from any machine. It builds
   the platform-neutral inputs (web bundle, cross-compiled Go backends) once,
   then dispatches each OS's packaging and signing to a declared builder: this
   machine, a GitHub Actions runner, or an SSH host. A builder runs
   **forge itself** (`forge builder run`) against a content-addressed job spec,
   so there is one build implementation whatever the transport. Inputs and
   outputs travel through the OCI registry by digest.
3. **A release is one OCI artifact** (`desktop.v1`) that holds every signed
   installer, blockmap and per-OS update-feed fragment. It goes in the
   existing ledger as an ordinary shared `oci` artifact. That needs no new
   closed-enum kind, and `forge release verify` already works for it. This is
   the precedent hosted static sites set (`static.v1`).
4. **A channel is an environment.** `desktop-alpha` and `desktop-latest` are
   envs whose desktop binding is `forge.OnDownloads {channel = ...}`.
   Publishing a release to a channel **is a promotion**: it gets the existing
   plan, approval digest, compare-and-set, gates and
   `--from desktop-alpha` path unchanged. Installers are write-once objects.
   The feed files are the only mutable state, the same split `OnBucket` makes
   between `releases/<digest>/` and `live/`.
5. **Secrets go through `Bundle.secret_provider`.** Every credential field in
   the schema is a secret **name**. Public facts (Apple team id, Azure account
   and publisher name, Cloudflare zone id) become reviewable KCL literals.
   Builders hold only signing credentials. Publish credentials exist only on
   the step that promotes. A later phase adds hosted, build-scoped grants
   redeemed with the runner's OIDC identity, so no CI system needs to store a
   signing secret.
6. **The plan learns about desktop releases.** It gains four finding codes.
   Three are stop-class: a signing-identity change (installed apps cannot
   apply the update), a dropped platform (that OS silently stops updating), and
   a prerelease promoted to a stable channel. The fourth, a version that is not
   ahead of the live one, is a warning.

The work comes in four phases: local build, remote builders and the release
cut, publication as promotion, then announcements, hosted secrets and
cutover. Each phase is useful by itself. Download URLs and feed names stay the
same throughout, so installed clients keep updating across the cutover.

---

## 1. Where things stand

### What runs today

Pushing a reliant `v*.*.*` tag runs `.github/workflows/release.yml`:

| Job                      | Runner         | Does                                                                                                                                             |
| ------------------------ | -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| `prepare`                | ubuntu         | Reads endpoints from `electron/release.config.json`; picks channel `alpha` or `latest` with a tag regex; builds `web/`; runs `verify-bundle.mjs` |
| `build-backend` (6 legs) | ubuntu         | Cross-compiles `cmd/reliant` for darwin/linux/windows × amd64/arm64 with endpoint ldflags                                                        |
| `release-macos`          | macos-latest   | electron-builder dmg+zip (arm64, x64); Developer ID signing; notarization; **publishes to R2**                                                   |
| `release-linux`          | ubuntu         | AppImage+deb (x64, arm64); **publishes to R2**                                                                                                   |
| `release-windows`        | windows-latest | nsis (x64, arm64) + portable (x64); Azure Trusted Signing; **publishes to R2**                                                                   |
| `finalize`               | ubuntu         | Purges a hand-written list of Cloudflare URLs; creates the GitHub Release; dispatches the Homebrew tap                                           |

electron-builder publishes with `provider: s3` to bucket `downloads` on R2. The
app reads its feed from `https://downloads.reliantlabs.io/` through the
`generic` provider. It picks its own channel with the same prerelease regex the
workflow uses (`electron/src/main.js:107`).

Control-plane's part is configuration only. `deploy/kcl/desktop_release.k`
renders `reliant_desktop_release_config` into reliant's committed
`electron/release.config.json`, and drift gates in both repos keep the two in
step. `lib/builds.k` defines `reliant_desktop_service`, a `kind = "tool"`
ShellBuild around `npm run dist`, and nothing calls it. The comment in
`desktop_release.k` says so plainly, after an earlier comment claimed
otherwise.

### What that shape has cost

Each item below is recorded in a code comment or the release skill, or was
measured on 2026-10-07:

- **v1.7.5 shipped without `VITE_CONTROL_PLANE_API_URL` or `VITE_GATEWAY_URL`.**
  A hand-written `.env` heredoc listed 15 of the 22 variables. Coupons threw
  "Control plane API URL not configured." `release.config.json` and its drift
  gate exist to fix this, which means a second copy of the config whose only
  job is to agree with the first.
- **v1.7.7: Windows clients were served 1.7.5 for about 20 hours.** The purge
  list named `latest-win.yml`, but electron-builder writes the Windows feed as
  `latest.yml`. Purging a URL that does not exist is a silent no-op.
- **v1.7.12 shipped no Windows build** (a delve build-tag sentinel), and
  **v1.7.14 shipped no macOS build**. In v1.7.14 the account holder changed,
  so signing still worked but notarization returned 403. Each platform job
  publishes on its own, so these became partial releases with no record of
  which platforms a version actually covers.
- **Tags v1.7.8 through v1.7.12 are not on main**, and `electron/package.json`
  has drifted from the highest tag before. The version is a fact about the
  bytes, and nothing checks that fact against anything.
- **Homebrew cask PRs #15–#26 had a corrupted `arch` mapping**, caused by a sed
  bug in the tap's update workflow. The checksums were recomputed by
  re-downloading bytes the release had just produced.
- **Feeds are served with `cache-control: max-age=86400`**, with
  `cf-cache-status: HIT` on `latest-mac.yml`. Whether a release reaches users
  within a day depends entirely on the purge being exactly right.
- **The alpha feeds have not moved since 1.6.4-rc1** (`releaseDate`
  2026-08-19). `latest` serves 1.7.16. Unless something writes `alpha-*.yml`,
  an alpha install polls a feed that will never offer 1.7.x. Whether
  electron-updater falls back to `latest` for those installs needs checking.
  Either way, nothing in the release process shows that the channel is stale.

### Where it falls outside forge's model

- **No ledger entry.** A desktop release is not a release in the forge sense:
  there is no immutable artifact set, no provenance, and no answer to "which
  bytes is the `latest` channel serving." `harvestFileArtifacts`
  (`internal/cli/release_artifacts.go`) names this gap. A file artifact has no
  URI because "nothing in KCL declares a destination."
- **No plan or approval.** Pushing a tag publishes to every user. The O-13
  rule is that the review happens before the write. It covers every cluster
  deploy and nothing that reaches a user's desktop.
- **Secrets are GitHub-only and over-broad.** `release.yml` references 29
  repository secrets. At least ten are not credentials: Apple team id, Azure
  tenant id and client id, the four Trusted Signing
  endpoint/account/profile/publisher-name values, the Cloudflare zone id, the
  R2 endpoint, and the API key id/issuer pair. Three more
  (`VITE_SUPABASE_URL`, `VITE_SUPABASE_ANON_KEY`, `SUPABASE_ANON_KEY`)
  duplicate values already public in `release.config.json`, and `SENTRY_DSN`
  and `VITE_SENTRY_DSN` are one value stored twice. Every packaging job holds
  R2 write credentials, including Linux, which signs nothing.
- **There are two build authorities.** `forge build <env> -t reliant-cli`
  reproduces the CLI binary from KCL. Nothing reproduces the desktop app
  outside GitHub.

## 2. Goals and non-goals

**Goals**

- A desktop app is declared in KCL. `forge env build <env>` produces it, and
  `forge env build <env> --release <v>` cuts it into the ledger.
- Per-OS packaging and signing runs where it has to, and forge collects and
  verifies the results.
- Publishing to a channel is a promotion with a plan, an approval and a ledger
  entry. Going from alpha to latest is `--from`.
- Signing and publishing credentials go through the env's secret provider.
  Builders get least privilege.
- Behavior does not change for installed clients: same URLs, same feed names,
  same signing identities.

**Non-goals for this proposal**

- Hosting downloads on the control plane (an `OnHosted` desktop runtime). The
  OCI release artifact is chosen partly so this can be added later.
- Mobile (iOS/Android) and store submission. The builder protocol and the
  channel model carry over, but the packager and publications are different
  work.
- Replacing electron-builder or electron-updater.
- Merging the Homebrew cask PR. The tap's ruleset requires a human, and this
  design keeps that requirement.

## 3. The mapping

The design follows from treating each desktop concept as a forge concept that
already exists:

| Desktop concept                                        | forge concept                                                |
| ------------------------------------------------------ | ------------------------------------------------------------ |
| One version's signed installers, blockmaps, feeds      | A **release** artifact: immutable, content-addressed         |
| The install base that polls `alpha-mac.yml`            | An **environment** (`desktop-alpha`)                         |
| Pointing a channel's feeds at a version                | A **promotion**: append-only, CAS, plan-gated                |
| "Ship what alpha has to stable"                        | `forge env deploy desktop-latest --from desktop-alpha`       |
| The macOS / Windows machine that packages and signs    | A **builder**: where one platform's build runs               |
| Apple cert, notary key, Azure secret, R2 key, CF token | Secret **references** resolved by `Bundle.secret_provider`   |
| Crash-free rate on alpha before promoting              | A **gate** recorded against the alpha promotion              |
| Endpoints baked into the renderer and main process     | `build_env`, which is part of the artifact's **variant** key |

## 4. Alternatives considered

The design makes five separate decisions. Each one lists the options and the
reasoning behind the choice.

### A. Who owns the pipeline

| Option                                                                                 | Verdict                                                                                                                                                                                                                                                                                                                                                                                        |
| -------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **A1. Status quo:** reliant's `release.yml` builds; forge projects config into it      | Rejected. This is the two-authority shape behind every incident in §1. The drift gate only treats the symptom.                                                                                                                                                                                                                                                                                 |
| **A2. forge generates `release.yml`** from KCL, as a Tier-1 workflow like `deploy.yml` | Partly adopted. A generated workflow gets the matrix and secret list from KCL, but the build logic would still be YAML that only GitHub can run. No laptop, SSH or hosted builder could reproduce it, and the release, plan and promotion would still be bolted on afterwards. What survives is the **builder endpoint** workflow (§5.3): a thin generated shim that runs `forge builder run`. |
| **A3. ShellBuild escape hatch** (`reliant_desktop_service`)                            | Rejected. The build is opaque to forge: no per-platform dispatch, no signing model, no artifact identity. In practice it was never wired up.                                                                                                                                                                                                                                                   |
| **A4. First-class `forge.DesktopApp` + builder primitive** (recommended)               | forge owns the build graph, release identity and publication. Electron-builder and the OS toolchains do what only they can do.                                                                                                                                                                                                                                                                 |

### B. Where per-OS builds run

| Option                                                                                                                                                  | Verdict                                                                                                                                                                                                                                                                                                                     |
| ------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **B1. CI matrix is the orchestrator**: each leg runs `forge desktop build --platform x`; a final job harvests with `--no-build`                         | Rejected as the primary path. That gives two code paths: `forge env build` on a laptop and the CI matrix. Two paths drift. It also ties orchestration to one CI vendor.                                                                                                                                                     |
| **B2. forge always orchestrates and dispatches to declared builders** (recommended)                                                                     | One path. The orchestrator can be a laptop, a Linux CI job or later the control plane. A builder is declared per platform, and its transport is an adapter. In CI the orchestrating job is a cheap ubuntu runner that waits on the macOS and Windows dispatches.                                                            |
| **B3. Cross-sign everything from Linux** (`rcodesign` for macOS signing and notarization; `jsign`/`osslsigncode` with Trusted Signing; NSIS under wine) | Rejected as the default. electron-builder only builds DMGs on macOS (`hdiutil`), does not drive `rcodesign`, and Squirrel.Mac validates the designated requirement. Signing that differs subtly from Apple's tools is a risk we cannot test cheaply. The builder abstraction leaves room for a `linux-cross` builder later. |
| **B4. Hosted macOS/Windows fleet on the control plane** (extend `RemoteBuild`)                                                                          | Deferred. This is the durable answer for a hosted product, but Apple licensing requires Apple hardware: EC2 mac instances have a 24-hour minimum allocation, or we would need a MacStadium-style provider. B2's protocol is designed so a hosted builder is one more adapter.                                               |

### C. How channels map

| Option                                                                               | Verdict                                                                                                                                                                                                                                                                                                         |
| ------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **C1. Channel = environment** (recommended)                                          | No ledger model change. Plan, CAS, approval digest, gates, `--from` and `env status` all apply as they are. "A place a release runs" describes an install base as well as it describes a cluster. Cost: one small env directory per channel, which `forge env new desktop-latest --from desktop-alpha` creates. |
| **C2. Channel as a field on the promotion** (`env` + `channel`)                      | Rejected. Promotions are keyed by env in both backends, and the hosted ledger enforces that in its schema. A second key touches the CAS, the row lock, `promote --from`, every status view and the control plane's database for a concept C1 already covers.                                                    |
| **C3. Channel derived from the version's prerelease suffix at publish time** (today) | Rejected. It is implicit, and it never moves a stable release onto a prerelease channel, which is how alpha came to sit at 1.6.4-rc1. Under C1, "alpha users get 1.7.17" is an explicit, visible promote.                                                                                                       |

### D. How a release records the installers

| Option                                                                                     | Verdict                                                                                                                                                                                                                                                                                                                                                                                                        |
| ------------------------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **D1. One OCI artifact (`desktop.v1`), recorded as a shared `oci` artifact** (recommended) | No change to the closed `Kind` set, so the hosted ledger's CHECK constraint stays the same. One digest is the identity. `release verify` is a cheap manifest check instead of re-downloading 2.5 GB. A failed cut leaves nothing on the public CDN. It is the same transport hosted static sites use, so hosted downloads come almost for free later. Cost: the installers are stored twice (registry and R2). |
| **D2. One `file` artifact per installer, with a `uri` on R2**                              | Rejected. About 15 ledger entries per release. The identity is a public, mutable bucket path. Verifying it means hashing every file again.                                                                                                                                                                                                                                                                     |
| **D3. A new `Kind` (`desktop`)**                                                           | Rejected. It is a closed-enum change in both backends and buys nothing D1 does not already give.                                                                                                                                                                                                                                                                                                               |

### E. How builders get secrets

| Option                                                                             | Verdict                                                                                                                                                                                                                                                                                  |
| ---------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **E1. Job spec carries values**                                                    | Rejected. `workflow_dispatch` inputs show up in the run UI and are not masked.                                                                                                                                                                                                           |
| **E2. `ExternalSecrets`: values are ambient on the builder** (phase 2)             | Adopted first. The names are declared in KCL, the generated builder workflow maps exactly those names, and GitHub environments scope each platform's secrets to that platform's job. forge checks presence before packaging starts.                                                      |
| **E3. `HostedSecrets` with build-scoped grants redeemed by runner OIDC** (phase 4) | The durable answer. The control plane releases exactly the declared signing secrets to an attested job for a pending build grant, and to no human credential. That keeps the rule that a self-managed env's secrets are never pullable, and it means no CI vendor has to store anything. |

## 5. Recommended design

### 5.1 Pipeline shape

```
 forge env build desktop-alpha --release desktop-1.8.0-rc.1
 ─────────────────────────────────────────────────────────────────────────────
 ORCHESTRATOR (any OS: laptop, ubuntu CI job, later the control plane)
   1. render KCL; resolve source pin → commit; read app version
   2. preflight: builders reachable, declared secrets present, version unused
   3. build platform-neutral INPUTS once (web bundle, 6 backend binaries)
   4. push inputs → <image>/staging @sha256:I
   5. dispatch one JOB per platform ─────────────┐
                                                 ▼
                         BUILDER (per OS, as declared)
                           forge builder run <job>
                             pull inputs @I, fetch source @commit
                             resolve signing secrets (provider)
                             electron-builder --<os> --publish never
                             sign / notarize / verify signatures
                             push outputs → <image>/staging @sha256:P_os
                             return result doc (files, hashes, feeds)
                                                 │
   6. collect results; verify every digest ◄─────┘
   7. assemble desktop.v1 manifest (blob mounts, no re-upload) → @sha256:R
   8. cut release: artifacts["reliant-desktop"] = oci shared @R
 ─────────────────────────────────────────────────────────────────────────────
 forge env deploy desktop-alpha desktop-1.8.0-rc.1
   plan → approve → promotion entry → PUBLISH (this machine):
     upload installers (write-once) → verify served → write alpha-*.yml
     → purge feeds → wait until feeds serve 1.8.0-rc.1 → announce
```

### 5.2 What runs locally and what is delegated

| Step                                                    | Runs on                   | Why                                                                                                                              |
| ------------------------------------------------------- | ------------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| Render, resolve pin, read version, preflight            | Orchestrator              | Needs only forge                                                                                                                 |
| Web bundle + bundle verification                        | Orchestrator              | Platform-neutral. Built **once**, so every OS ships byte-identical renderer assets, which can be checked by asserting one digest |
| Go backend cross-compilation (CGO=0, 6 targets)         | Orchestrator              | Platform-neutral; Linux runner minutes are the cheapest                                                                          |
| Packaging (dmg/zip, nsis/portable, AppImage/deb)        | Builder for that OS       | DMG creation needs macOS; Trusted Signing via electron-builder uses a Windows PowerShell module                                  |
| Signing, notarization, stapling, signature verification | Builder for that OS       | The keychain and `notarytool`/`stapler` need macOS; `signtool` and Authenticode checks need Windows                              |
| Assembly, release cut                                   | Orchestrator              | Holds the registry push; builders' blobs are mounted, not re-uploaded                                                            |
| Publish: R2 upload, feeds, CDN purge, announcements     | Whoever runs `env deploy` | The **only** step that holds publish credentials. Builders never do.                                                             |

Linux packaging can run on the orchestrator when the orchestrator is Linux. A
`LocalBuilder` declared for a platform whose OS does not match the host is
refused, with the error naming the platform and the host OS.

### 5.3 The builder protocol

A builder takes a **job spec** and returns a **result document** plus output
blobs. That is the whole contract. Transports only carry it.

**Job spec** (JSON, content-addressed by its own sha256, and **secret-free**):

```json
{
  "schema": "forge.dev/desktop-job/v1",
  "job_id": "dj-7f3c…",
  "forge": { "version": "v0.1.45", "commit": "e36b9e51…" },
  "app": "reliant-desktop",
  "source": {
    "repo": "github.com/reliant-labs/reliant",
    "ref": "v1.8.0-rc.1",
    "commit": "a1b2…"
  },
  "inputs": { "ref": "us-central1-docker.pkg.dev/…/desktop/staging@sha256:I…" },
  "packager": {
    "type": "electron-builder",
    "dir": "electron",
    "config": "electron-builder.common.js"
  },
  "platform": {
    "os": "darwin",
    "targets": { "dmg": ["arm64", "x64"], "zip": ["arm64", "x64"] }
  },
  "build_env": { "VITE_API_URL": "https://api.reliantapi.com", "…": "…" },
  "secrets": ["CSC_LINK", "CSC_KEY_PASSWORD", "APPLE_API_KEY", "SENTRY_DSN"],
  "signing": {
    "type": "apple",
    "team_id": "…",
    "notary": { "type": "api-key", "key_id": "…", "issuer": "…" }
  },
  "output": { "repository": "us-central1-docker.pkg.dev/…/desktop/staging" }
}
```

**`forge builder run --job <spec>`** is a hidden subcommand. It:

1. Refuses unless its own forge version and commit equal the spec's. A builder
   running a different forge would make a different artifact.
2. Pulls inputs by digest, then fetches the source pin with `internal/gitsource`
   (the same cache and the same `.forge/source-overrides.yaml` rules).
3. Resolves `secrets` through the env's provider (§5.4) and fails before
   packaging if any are missing, listing every missing name and where it is
   expected to live.
4. Runs `electron-builder --<os> <target>:<arch>… --publish never`. The targets
   come from the job, so KCL is the only place they are declared.
5. Verifies what it signed. On macOS: `codesign --verify --deep --strict`,
   `spctl -a -t open --context context:primary-signature`, and
   `stapler validate`. On Windows: Authenticode status and the publisher CN.
   The verdicts go in the result doc.
6. Pushes the outputs as an OCI manifest and returns the result doc: each file's
   name, OS/arch/target, size, sha256 and sha512 (the hash electron-updater
   uses), its blockmap, the feed fragments electron-builder wrote
   (`latest-mac.yml`, `latest.yml`, `latest-linux*.yml`), the signing verdicts,
   and the run URL.

**Transports (adapters):**

| Builder                      | Dispatch                                                                                                                                                            | Inputs / outputs    | Notes                                                                                                                                                                          |
| ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `forge.LocalBuilder`         | In-process                                                                                                                                                          | Local paths         | Dev builds, and Linux on a Linux orchestrator                                                                                                                                  |
| `forge.GitHubActionsBuilder` | `workflow_dispatch` of the generated `forge-builder.yml` with `job_id`, base64 `job`, `runs_on` and `environment`; correlated by `run-name: forge-builder <job_id>` | Registry, by digest | Polls until done; Ctrl-C cancels the run; prints the run URL immediately. `GITHUB_TOKEN` can dispatch `workflow_dispatch`, so a CI orchestrator needs no PAT for its own repo. |
| `forge.SSHBuilder`           | `ssh host forge builder run`                                                                                                                                        | Registry, by digest | A Mac mini under a desk. `forge builder doctor` checks that forge on the host is at the pinned version.                                                                        |
| Hosted (future)              | Control-plane build RPC                                                                                                                                             | Registry, by digest | The same job spec is what turns `RemoteBuild` into something real for desktops                                                                                                 |

**The registry carries all artifacts.** Inputs and outputs move by digest in
both directions, so every transport behaves the same. A retry is cheap: a
platform whose job-spec digest already has a successful result in staging is
not rebuilt. If notarization times out on macOS, re-running the cut repackages
only macOS.

**The generated builder workflow.** `forge generate` writes
`.github/workflows/forge-builder.yml` (Tier-1, banner and `forge:hash`) when
an env declares a GitHub Actions builder in **this** repository. It installs
forge at the version in the job spec and runs `forge builder run`. Its `env:`
block has exactly one line per secret name any builder declares, generated from
KCL, so the list is never maintained by hand. `environment:
${{ inputs.environment }}` means only a platform's own GitHub environment
supplies values, so the Linux job never sees Apple credentials. When the
builder repo is **another** repository (see open question 2), forge cannot
write there. `forge builder workflow --print` prints the file, and preflight
reads the remote file's `forge:hash` through the API and refuses a stale shim.

### 5.4 Secrets

**Convention:** every credential field in the desktop schema is a secret
**name** (a string), resolved through the env's `Bundle.secret_provider`. Every
other field is a public literal and is reviewed in diffs.

| Credential                     | Consumer                       | Field                                               |
| ------------------------------ | ------------------------------ | --------------------------------------------------- |
| Developer ID .p12 + password   | darwin builder                 | `AppleSigning.certificate`, `.certificate_password` |
| App Store Connect .p8          | darwin builder                 | `AppleNotaryKey.key`                                |
| Azure service principal secret | windows builder                | `AzureTrustedSigning.client_secret`                 |
| Sentry DSN, Statsig key        | every builder (baked at build) | `DesktopApp.build_secrets`                          |
| R2 access key + secret         | publisher (`env deploy`)       | `S3Bucket.access_key_id`, `.secret_access_key`      |
| Cloudflare API token           | publisher                      | `CloudflareCDN.token`                               |
| Tap token, GitHub token        | publisher (announce)           | `HomebrewCask.token`, `GitHubRelease.token`         |

How each provider resolves on each consumer:

| Provider          | On a builder                                                                                                                         | On the publisher                    |
| ----------------- | ------------------------------------------------------------------------------------------------------------------------------------ | ----------------------------------- |
| `FileSecrets`     | The YAML store (dev/e2e only, as today; for local signing experiments)                                                               | Same                                |
| `ExternalSecrets` | The builder's process environment. Presence is checked before packaging, and a miss names the GitHub environment expected to hold it | The publisher's process environment |
| `HostedSecrets`   | **Phase 4:** a build-scoped grant (below)                                                                                            | **Phase 4:** a publish-scoped grant |

**Build-scoped grants (phase 4).** Today `HostedSecrets` values for a
persistent or self-managed env are write-only, and they should stay that way
for humans. A grant adds one narrow reader. When the orchestrator dispatches a
job, it asks the control plane to open a grant bound to (env, release, job
digest, secret names, builder identity, about one hour). The runner redeems the
grant by presenting its own OIDC token. The control plane checks the token's
`repository`, `workflow_ref` and `job_workflow_ref` claims against the declared
builder, and the `job_id` against the open grant. It then returns exactly
those values, once. Nothing is in the dispatch inputs, nothing is stored in
GitHub, and no human credential can redeem a grant. This needs control-plane
work (an OIDC trust config per org, plus a grant table), so it is scheduled
last and nothing earlier depends on it.

`forge secret list --env desktop-alpha` lists every declared signing and
publish name. `forge builder doctor desktop-alpha` dispatches a no-op job to
each builder and reports, per builder, which names are present. For
`ExternalSecrets` that is the only way to learn presence before a 30-minute
build.

### 5.5 The release artifact

`desktop.v1` is one OCI image manifest:

- **config** (`application/vnd.forge.desktop.v1+json`): app name, app version,
  `appId`, product name, source pin (repo, ref, commit), inputs digest, variant
  key, per-file records (name, os, arch, target, size, sha256, sha512,
  blockmap), the feed fragments **exactly as electron-builder wrote them**,
  per-platform signing identity (Apple team id, Windows publisher CN) and
  verification verdicts, and each builder's run URL.
- **layers**: one per file. These are blob-mounted from the staging repository
  in the same registry, so assembling a release uploads nothing new.

In the ledger it is an ordinary artifact:

```json
"reliant-desktop": {
  "kind": "oci", "mode": "shared",
  "digests": { "*": "sha256:…" },
  "platforms": ["darwin/arm64", "darwin/amd64", "linux/amd64", "linux/arm64", "windows/amd64", "windows/arm64"],
  "uri": "us-central1-docker.pkg.dev/reliant-labs-475814/reliant/desktop"
}
```

**Invariants enforced at the cut** (each is a refusal, not a warning):

1. **Completeness.** Every declared platform produced every declared target.
   This is the same rule `forge env build --release` already applies, and it
   is deliberately different from today's behavior, where a failed platform
   still ships the others. To ship without Windows, remove Windows from the
   declaration. That is a reviewable diff, not a side effect of a failed job.
2. **One version, one byte set.** Within a project's ledger, a given (app,
   app version, variant) maps to exactly one digest. electron-updater compares
   versions and skips same-version updates. Installer file names carry the
   version. Homebrew pins the sha256. A version that named two byte sets would
   break all three, so a re-cut with different bytes fails the same way
   `ErrReleaseConflict` does today.
3. **The version is read, never invented.** It comes from the packager's
   `version_file` at the source pin. When the source ref is a tag like `vX`,
   the plan warns if the version is not `X`, which catches the
   `package.json` / tag drift.
4. **Variant key.** A sha256 over the canonical `build_env`. Two envs with equal
   `build_env` (alpha and latest, both on prod endpoints) share bytes and can
   promote between each other. Promoting a release that has no variant for the
   target env's `build_env` is refused, and the error names
   `--bundle-envs` as the fix. `build_secrets` values are assumed to be the
   same within a variant (see open question 7).

### 5.6 Channels and publication

A channel env binds the app to `forge.OnDownloads`. The apply step that runs
after a promotion:

1. **Upload installers and blockmaps, write-once.** If an object already exists
   with the same sha256, skip it. If it exists with a different sha256,
   refuse. Each object gets its declared `Cache-Control` (default:
   `public, max-age=31536000, immutable`).
2. **Verify the files are served.** `HEAD` each file through `base_url` and
   compare `content-length`. This replaces release.yml's hand-listed curl
   loop. The file names come from the manifest, so the `latest-win.yml` class
   of bug cannot happen.
3. **Write the feeds**: electron-builder's own fragment bytes, renamed per
   channel (`<channel>-mac.yml`, `<channel>.yml`, `<channel>-linux.yml`,
   `<channel>-linux-arm64.yml`). The fragment content has no channel field, so
   the rename is all forge does. A golden test pins that fact and will fail if
   a future electron-builder adds a channel-dependent field. forge does **not**
   reimplement the feed format. Feeds default to
   `public, max-age=300, must-revalidate`, so a missed purge costs minutes,
   not a day.
4. **Purge the feeds only.** Versioned files have new names and need no purge.
   `invalidate = "all"` is still available as a deliberate choice.
5. **Wait.** `GET` each feed through `base_url` until it serves the new version,
   or `--timeout`. This is the health gate, the same as for every other deploy.
6. **Announce** (only where the env declares it). The Homebrew cask is rendered
   from the ledger: version plus the sha256 of each `.dmg`, which forge already
   holds. No re-download and no sed. forge opens or updates a PR and **never
   merges it**. A GitHub Release is created only if the tag exists and is an
   ancestor of main, the same check `release-tag-guard.yml` makes.

A publish that fails partway leaves nothing users can see. Files uploaded
before the feeds were written are referenced by nothing. Re-running the deploy
is idempotent. "Re-deploying the release an env already runs appends nothing
and still applies" covers finishing a half-done publish, re-running a failed
announcement, and re-purging.

**The URL layout does not change.** `prefix = ""` keeps installers and feeds at
the bucket root under the current names, so installed 1.7.x clients and the
Homebrew cask keep working across the cutover. A versioned prefix
(`releases/<version>/`) would also work, because feed `url`s resolve relative
to the feed. That is a later, separate decision.

### 5.7 Ledger, plan and approval

Nothing in the promotion machinery is new. Desktop envs reach it through these
changes:

- **Shape.** The desktop binding projects its **feeds** as shape objects
  (`publish.forge.dev/v1 DesktopFeed`, keyed by store and object name, hashed
  over the feed bytes). Installers are artifacts, not objects, in the same
  sense that images are artifacts and manifests are objects. That keeps old
  versions' files from showing up as `object_removed`. Shape also gains a small
  `ShapeDesktop` summary per app: channel, whether the channel accepts
  prereleases, app version, appId, per-OS signing identity, platform set and
  artifact key. `BuildPlan` stays I/O-free, and the hosted backend recomputes
  the same plan from what it stores.
- **Plan.** There is one new section, `desktop`, and four new finding codes
  added to the closed sets in `pkg/release`:

  | Code                           | Class | Fires when                                                                                                                                                                              |
  | ------------------------------ | ----- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
  | `desktop_identity_change`      | stop  | appId, Apple team id or Windows publisher CN differs from the live release. Squirrel.Mac and electron-updater's Windows signature check will refuse the update for every installed app. |
  | `desktop_platform_dropped`     | stop  | The live channel serves an OS feed that the candidate does not. That OS's installs stop updating with no error.                                                                         |
  | `desktop_prerelease_on_stable` | stop  | A semver prerelease is promoted to a channel declared `prerelease = False`                                                                                                              |
  | `desktop_version_behind`       | warn  | The candidate's app version is not ahead of the live one. Installed clients ignore it; only new downloads get it.                                                                       |

  Stop codes need `--acknowledge-destructive <code>` even with `--yes`.
  `desktop_identity_change` is the desktop counterpart of
  `lb_identity_change`. The v1.7.14 account-holder change is the kind of event
  it exists to stop.

- **Drift is observable.** Feeds are public. `forge env status desktop-latest`
  fetches each feed through `base_url`, shows the served version per OS next to
  the ledger's binding, and reports a hand-edited feed as `drift`.
- **Gates.** `forge gate record desktop-alpha --from crash-rate.json` attaches
  soak evidence to the alpha promotion, and `--gate` freezes pre-promote
  evidence into the latest promotion. "Was this known before the button was
  pressed" gets an answer for desktop releases too.
- **The two-stage pipeline is unchanged.** `--plan-only --json`, then
  `--approve <digest>` in a job behind a GitHub environment protection rule.
  Phase 4 generates this workflow for control-plane.
- **Where the ledger lives is unchanged.** A desktop env that declares
  `forge.ControlPlane` records there. Otherwise the machine ledger keyed by
  project records it.

**Env kind fix.** `runsOnOwnCluster` (`internal/cli/hosted_env_resolver.go`)
counts only cluster workloads and non-hosted databases. So an env whose only
bindings publish to a store the author operates (`OnDownloads`, and already
`OnBucket`/`OnFirebase` frontends) is classified **local**. With a control
plane declared, that makes its R2 and Cloudflare credentials pullable. Such an
env is **self_managed**: forge applies it, and its secrets are write-only. This
is a defect today for bucket-only frontend envs, and it is fixed in phase 3
whether or not the rest of this proposal lands.

## 6. KCL schema

The new schemas go in a new `kcl/desktop.k`, re-exported as `forge.*` like
`kcl/workload.k`. `Bundle` gains `desktops: [DesktopApp] = []`, and the render
emits `output.desktops[]`, dispatched on `runtime.type`.

```python
schema DesktopApp:
    """A packaged desktop application: per-OS installers built from one
    source, signed per platform, released as ONE immutable OCI artifact,
    and published to a channel's download store and update feed.

    Declared once (deploy/kcl/desktops.k) and bound per env with
    `runtime`, exactly like a workload or a frontend."""
    name: str
    # OCI repository for the release artifact. Like every image, it names
    # its registry host. Platform outputs are staged under `<image>/staging`,
    # in the SAME registry, so assembling a release is a blob mount.
    image: str
    # Exactly one. `source` pins another repository (reproducible in CI);
    # `path` is inside this project (pinned by this project's own commit).
    source?: GitSource
    path?: str
    packager: ElectronBuilder
    # Platform-neutral inputs built ONCE by the orchestrator and staged to
    # every platform build by digest.
    inputs: [DesktopInput] = []
    platforms: [DesktopPlatform]
    # PUBLIC build-time config baked into the bytes (endpoints, publishable
    # keys). Its digest is the artifact's VARIANT key.
    build_env: {str: str} = {}
    # Build-time SECRET names (a Sentry DSN). Resolved through the env's
    # secret_provider on each builder; never rendered.
    build_secrets: [str] = []
    runtime?: OnHost | OnDownloads | BuildOnly

    check:
        bool(source) != bool(path), "DesktopApp '${name}': set exactly one of source (a GitSource pin) or path (inside this project)"
        not path or not path.startswith(".."), "DesktopApp.path must stay inside the project — use source = forge.GitSource {...} for another repository"
        len(platforms) > 0, "DesktopApp '${name}' declares no platforms"
        len({p.os: None for p in platforms}) == len(platforms), "DesktopApp '${name}': each os may be declared once"

schema ElectronBuilder:
    type: "electron-builder" = "electron-builder"
    # Directory holding package.json and the electron-builder config,
    # relative to the source root.
    dir: str = "."
    # Packaging details only (icons, entitlements, nsis options). Targets,
    # arch and publish are forge's: passed on the command line, which wins
    # over the file, so they are declared once — here.
    config: str = "electron-builder.config.js"
    # The app version is a fact about the bytes (app.getVersion()), so forge
    # READS it from here. It never invents or bumps one.
    version_file: str = "package.json"

schema DesktopInput:
    """Built once on the orchestrator, in the source tree, with build_env
    and build_secrets in its environment; then staged to every builder."""
    name: str
    build: ShellBuild | GoBuild
    # Produced path, relative to the source root; restored at the same
    # path on each builder.
    output: str
    # Only these OSes receive it (a darwin backend is useless on Windows).
    # Empty = all.
    platforms: [str] = []

schema DesktopPlatform:
    os: "darwin" | "windows" | "linux"
    # electron-builder target → arch list. Exact, because a missing arch is
    # a missing download and the completeness check needs to know it.
    #   {dmg = ["arm64", "x64"], zip = ["arm64", "x64"]}
    #   {nsis = ["x64", "arm64"], portable = ["x64"]}
    targets: {str: [str]}
    builder: LocalBuilder | GitHubActionsBuilder | SSHBuilder
    signing?: AppleSigning | AzureTrustedSigning
    # A platform bound to OnDownloads with no signing is refused unless it
    # says so. Unsigned is a decision, never a default.
    unsigned: bool = False

    check:
        len(targets) > 0, "DesktopPlatform '${os}' declares no targets"
        signing or unsigned or os == "linux", "DesktopPlatform '${os}' has no signing — declare signing, or unsigned = True for a dev/PR build"

# ── Builders ────────────────────────────────────────────────────────────

schema LocalBuilder:
    """This machine. Refused when its OS is not the platform's."""
    type: "local" = "local"

schema GitHubActionsBuilder:
    type: "github-actions" = "github-actions"
    # Repository hosting forge's generated builder workflow. It need not be
    # this project's: a public repository's runners are free, a private
    # one's macOS minutes are billed. A one-line decision, made here.
    repo: str
    # "macos-15", "windows-2025", "ubuntu-24.04", or a self-hosted label.
    runs_on: str
    workflow: str = "forge-builder.yml"
    # GitHub environment the job runs in — scopes WHICH secrets the runner
    # can see, so the Linux job never receives Apple credentials.
    environment?: str
    # Secret NAME holding a token that can dispatch and read runs on `repo`.
    token: str = "GITHUB_TOKEN"
    timeout_minutes: int = 60

schema SSHBuilder:
    type: "ssh" = "ssh"
    host: str
    user?: str

# ── Signing ─────────────────────────────────────────────────────────────

schema AppleSigning:
    type: "apple" = "apple"
    team_id: str                                    # public
    certificate: str = "CSC_LINK"                   # secret NAME (base64 .p12)
    certificate_password: str = "CSC_KEY_PASSWORD"  # secret NAME
    notary: AppleNotaryKey | AppleNotaryAppleID = AppleNotaryKey {}

schema AppleNotaryKey:
    """A team credential: survives an account-holder change (v1.7.14)."""
    type: "api-key" = "api-key"
    key: str = "APPLE_API_KEY"   # secret NAME (.p8 contents)
    key_id?: str                 # identifier, not a credential
    issuer?: str                 # identifier, not a credential

schema AppleNotaryAppleID:
    type: "apple-id" = "apple-id"
    apple_id: str = "APPLE_ID"                              # secret NAME
    password: str = "APPLE_APP_SPECIFIC_PASSWORD"           # secret NAME

schema AzureTrustedSigning:
    type: "azure-trusted-signing" = "azure-trusted-signing"
    endpoint: str              # e.g. https://eus.codesigning.azure.net
    account: str
    certificate_profile: str
    # MUST equal the certificate CN: electron-updater verifies it on every
    # Windows update, so changing it is `desktop_identity_change`.
    publisher_name: str
    tenant_id: str
    client_id: str
    client_secret: str = "AZURE_CLIENT_SECRET"   # secret NAME

# ── Publication ─────────────────────────────────────────────────────────

schema OnDownloads:
    """Publish to a download store the author operates, and point one
    update channel's feeds at the release."""
    type: "downloads" = "downloads"
    # The feed name the app polls: latest, alpha, beta, ...
    channel: str
    # Does this channel accept semver prereleases?
    prerelease: bool = False
    store: S3Bucket | GCSBucket
    # Public URL clients fetch from; feed `url`s resolve against it.
    base_url: str
    prefix: str = ""
    cdn?: CloudflareCDN | StaticSiteCDN
    feed: ElectronUpdaterFeed = ElectronUpdaterFeed {}
    # First match wins. Feeds short, installers immutable: a missed purge
    # then costs minutes, not a day.
    cache_control: [CacheRule] = [
        CacheRule {pattern = "*.yml", cache_control = "public, max-age=300, must-revalidate"}
        CacheRule {pattern = "**", cache_control = "public, max-age=31536000, immutable"}
    ]
    announce: [HomebrewCask | GitHubRelease] = []

    check:
        channel, "OnDownloads.channel is required (the feed name the app polls, e.g. 'latest')"
        base_url.startswith("https://") and base_url.endswith("/"), "OnDownloads.base_url must be an https URL ending in '/'"

schema S3Bucket:
    """Any S3-compatible store (Cloudflare R2, AWS S3, MinIO)."""
    type: "s3" = "s3"
    endpoint: str                                     # an address, not a credential
    bucket: str
    region: str = "auto"
    access_key_id: str = "S3_ACCESS_KEY_ID"           # secret NAME
    secret_access_key: str = "S3_SECRET_ACCESS_KEY"   # secret NAME

schema GCSBucket:
    type: "gcs" = "gcs"
    bucket: str   # ambient ADC, as OnBucket today

schema CloudflareCDN:
    type: "cloudflare" = "cloudflare"
    zone_id: str                           # public
    token: str = "CLOUDFLARE_API_TOKEN"    # secret NAME
    invalidate: "feeds" | "none" | "all" = "feeds"

schema ElectronUpdaterFeed:
    """electron-updater's <channel>[-<os>].yml. The bytes are
    electron-builder's own, renamed per channel; forge does not reimplement
    the format. A union so Sparkle / Tauri feeds can join."""
    type: "electron-updater" = "electron-updater"

schema HomebrewCask:
    """Opens or updates a PR rendered from the ledger. Never merges: the
    tap's ruleset decides that."""
    type: "homebrew-cask" = "homebrew-cask"
    tap: str                          # github.com/reliant-labs/homebrew-reliant
    cask: str
    path?: str                        # default Casks/<cask>.rb
    token: str = "HOMEBREW_TAP_TOKEN" # secret NAME

schema GitHubRelease:
    type: "github-release" = "github-release"
    repo: str
    # Must exist and be an ancestor of the repo's default branch.
    tag: str
    notes_file?: str
    token: str = "GITHUB_TOKEN"       # secret NAME
```

### Example: control-plane adoption

`deploy/kcl/desktops.k` declares the app once:

```python
import forge
import lib.env as envlib

# Reliant's tag. Kept equal to the reliant-web GitSource ref and the go.mod
# require by a control-plane KCL check, so the desktop app, the SPA and the
# API they talk to are one release.
RELIANT_REF = "v1.8.0-rc.1"

_gha = lambda runs_on: str, environment: str -> forge.GitHubActionsBuilder {
    forge.GitHubActionsBuilder {
        repo = "github.com/reliant-labs/reliant"   # see open question 2
        runs_on = runs_on
        environment = environment
    }
}

reliant_desktop = lambda env: str -> forge.DesktopApp {
    forge.DesktopApp {
        name = "reliant-desktop"
        image = "us-central1-docker.pkg.dev/reliant-labs-475814/reliant/desktop"
        source = forge.GitSource {repo = "github.com/reliant-labs/reliant", ref = RELIANT_REF}
        packager = forge.ElectronBuilder {dir = "electron", config = "electron-builder.common.js"}
        inputs = [
            forge.DesktopInput {
                name = "web"
                build = forge.ShellBuild {cwd = "web", cmd = "npm ci && NODE_ENV=production npm run build && node ../.github/scripts/verify-bundle.mjs --dist dist"}
                output = "web/dist"
            }
            forge.DesktopInput {
                name = "backend"
                build = forge.ShellBuild {cmd = "PARALLEL=true ./scripts/build-electron.sh all"}
                output = "electron/resources/server"
            }
        ]
        platforms = [
            forge.DesktopPlatform {
                os = "darwin"
                targets = {dmg = ["arm64", "x64"], zip = ["arm64", "x64"]}
                builder = _gha("macos-15", "desktop-darwin")
                signing = forge.AppleSigning {team_id = "<team>", notary = forge.AppleNotaryKey {key_id = "<id>", issuer = "<issuer>"}}
            }
            forge.DesktopPlatform {
                os = "windows"
                targets = {nsis = ["x64", "arm64"], portable = ["x64"]}
                builder = _gha("windows-2025", "desktop-windows")
                signing = forge.AzureTrustedSigning {endpoint = "<endpoint>", account = "<account>", certificate_profile = "<profile>", publisher_name = "<CN>", tenant_id = "<tenant>", client_id = "<client>"}
            }
            forge.DesktopPlatform {
                os = "linux"
                targets = {AppImage = ["x64", "arm64"], deb = ["x64", "arm64"]}
                builder = _gha("ubuntu-24.04", "desktop-linux")
            }
        ]
        # The same projection release.config.json carries today — now read
        # live, so that file and its drift gate are retired.
        build_env = envlib.reliant_desktop_build_env(env)
        build_secrets = ["SENTRY_DSN", "STATSIG_CLIENT_KEY"]
    }
}
```

Each channel is a small env. This is `deploy/kcl/desktop-alpha/main.k`:

```python
import forge
import ..desktops as d

_downloads = forge.OnDownloads {
    channel = "alpha"
    prerelease = True
    store = forge.S3Bucket {endpoint = "https://<account>.r2.cloudflarestorage.com", bucket = "downloads"}
    base_url = "https://downloads.reliantlabs.io/"
    cdn = forge.CloudflareCDN {zone_id = "<zone>"}
}

output = forge.render(forge.Bundle {
    project = "control-plane"
    secret_provider = forge.ExternalSecrets {}   # HostedSecrets in phase 4
    desktops = [d.reliant_desktop("prod") | {runtime = _downloads}]
})
```

`desktop-latest` differs only in `channel = "latest"`, `prerelease = False` and
`announce = [forge.HomebrewCask {...}, forge.GitHubRelease {...}]`.
`desktop-local` binds `forge.OnHost {}`, which builds for the host platform with
a `LocalBuilder` and launches the result. That replaces
`reliant_desktop_service(env, "local")` and `dist:mac:prod-local`.

## 7. CLI

No new top-level noun. Desktop work goes through the env verbs that already
exist, plus a small `forge builder` group.

```text
forge build desktop-local                          # compile-only: host platform, launchable
forge env up desktop-local                         # build + launch (the dogfood loop)

forge env build desktop-alpha                      # every platform, on its declared builder; nothing published
forge env build desktop-alpha --platform darwin    # one platform
forge env build desktop-alpha --builder local      # override builders for a dev build; REFUSED with --release

forge env build desktop-alpha --release desktop-1.8.0-rc.1 --plan
                                                   # preflight: pin resolves, version read and unused,
                                                   # builders reachable, shims current, secrets present
forge env build desktop-alpha --release desktop-1.8.0-rc.1
                                                   # dispatch → collect → verify → assemble → cut

forge env deploy desktop-alpha desktop-1.8.0-rc.1  # plan → approve → publish → wait → announce
forge env deploy desktop-latest --from desktop-alpha
forge env deploy desktop-alpha --from desktop-latest   # stable → alpha, explicitly (fixes the stranded feed)
forge env deploy desktop-latest desktop-1.7.17 --plan-only --json   # stage one of a two-stage pipeline

forge env status desktop-latest                    # bound release, served version per OS, drift
forge release verify desktop-1.8.0-rc.1            # manifest exists at its digest
forge secret list --env desktop-alpha              # signing + publish names and presence

forge builder doctor desktop-alpha                 # no-op job per builder: reachable, forge version, secrets
forge builder workflow --print                     # the generated shim, for a builder repo forge can't write
forge builder run --job <spec>                     # hidden: what a builder executes
```

A cut, as printed:

```text
$ forge env build desktop-alpha --release desktop-1.8.0-rc.1
[desktop] reliant-desktop 1.8.0-rc.1  source github.com/reliant-labs/reliant@v1.8.0-rc.1 (a1b2c3d4)
[desktop] variant cfg-9e1f04ab  (build_env: 17 keys)
[desktop] inputs   web ✓ 41.2 MB   backend ✓ 6 binaries   → staging@sha256:5d0c…
[desktop] darwin   → github-actions macos-15   https://github.com/reliant-labs/reliant/actions/runs/…
[desktop] windows  → github-actions windows-2025   https://github.com/…/runs/…
[desktop] linux    → github-actions ubuntu-24.04   https://github.com/…/runs/…
[desktop] linux    ✓ 4 files   unsigned (declared)                       3m12s
[desktop] windows  ✓ 3 files   signed CN=<publisher> ✓                   9m48s
[desktop] darwin   ✓ 4 files   signed <team> ✓ notarized ✓ stapled ✓     21m05s
[desktop] assembled desktop.v1 @sha256:c7a9…  (15 files, 2.41 GB, 0 bytes re-uploaded)
[release] desktop-1.8.0-rc.1 cut: reliant-desktop oci @sha256:c7a9…
next: forge env deploy desktop-alpha desktop-1.8.0-rc.1
```

A promotion plan:

```text
$ forge env deploy desktop-latest desktop-1.8.0
plan  desktop-latest  (self_managed · machine ledger)
  release      desktop-1.7.17 → desktop-1.8.0           direction AHEAD
  desktop      reliant-desktop 1.7.17 → 1.8.0           channel latest (stable)
    identity   appId com.reliantlabs.reliant · team <team> · CN <publisher>   unchanged
    platforms  darwin arm64,x64 · windows x64,arm64 · linux x64,arm64        unchanged
    files      +15 (write-once; 0 already present)
    feeds      latest-mac.yml  latest.yml  latest-linux.yml  latest-linux-arm64.yml   4 changed
    announce   homebrew cask reliant 1.7.17 → 1.8.0 (opens PR) · GitHub release v1.8.0
  findings
    info  object_changed  downloads/latest-mac.yml  (+3 more)
  digest  sha256:4be1…
Proceed? [y/N]
```

## 8. What this retires

After cutover (phase 4) and one clean release:

- **reliant:** the build-and-publish path of `.github/workflows/release.yml`
  (a PR-only `pack:pr` smoke can stay), `.github/actions/generate-build-config`
  (its script stays as the packager's own pre-step, with env from forge),
  `.github/scripts/sync-release-config.mjs`, `electron/release.config.json`,
  `electron/scripts/with-release-config.mjs`, the `publish` blocks in
  `electron-builder.js` / `electron-builder.alpha.js`, the tag-regex channel
  detection, and the GitHub secrets that are not credentials.
- **control-plane:** `deploy/kcl/desktop_release.k`, `reliant_desktop_service`
  and `_DESKTOP_PACK_TARGET` in `lib/builds.k`, and the release-config drift
  gate in `ci.yml`.
- **The release skill's** manual desktop verification steps: `curl -sI` on
  DMGs, reading `latest-mac.yml`, checksumming DMGs for the cask PR. These
  become `forge env status` and the cask announcer.

What stays the same: reliant's two-phase tagging (`release.sh`,
`release-tag.sh`, `release-tag-guard.yml`), `package.json` as the version's
source, electron-builder and electron-updater, R2 and Cloudflare, every public
URL, and both signing identities.

## 9. Phased plan

Each phase ships on its own and leaves the system working. Tasks in a phase
without a listed dependency can run in parallel. Sizes: S ≤ 2 days,
M ≤ 1 week, L > 1 week.

### Phase 1: declare it, build it locally

Nothing is signed or published yet. The local channel moves onto forge.

| #   | Task                                                                                                                                                                                                   | Size | Depends         |
| --- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---- | --------------- |
| 1.1 | `kcl/desktop.k`: `DesktopApp`, `ElectronBuilder`, `DesktopInput`, `DesktopPlatform`, `LocalBuilder`; `Bundle.desktops`; render to `output.desktops[]`; closed-schema negative tests under `kcl/tests/` | M    |                 |
| 1.2 | Go decode (`KCLEntities.Desktops`, runtime discriminators); `forge project shapes --kind desktop`                                                                                                      | S    | 1.1             |
| 1.3 | Job spec type + `forge builder run` (hidden): source fetch via `internal/gitsource`, input staging, electron-builder invocation with targets on the command line, result doc                           | M    | 1.2             |
| 1.4 | Orchestrator in `forge build` / `forge env build`: inputs built once, per-platform jobs, `--platform`, `--builder local`, host-OS refusal, summary output                                              | M    | 1.3             |
| 1.5 | `OnHost` / `BuildOnly` desktop runtimes: `forge env up` launches the built app with `listen_ports = []` semantics                                                                                      | S    | 1.4             |
| 1.6 | Skill `deploy/desktop` + `docs/concepts.md` §6 row                                                                                                                                                     | S    | 1.1             |
| 1.7 | control-plane: `desktops.k`, `desktop-local` env, `reliant_desktop_build_env`; delete `reliant_desktop_service`                                                                                        | S    | 1.5 + forge pin |

**Exit:** `forge env up desktop-local` produces and launches the same app as
`npm run dist:mac:prod-local`, and `verify-bundle.mjs` passes on its renderer.

### Phase 2: remote builders, signing, the release cut

Signed artifacts are recorded in the ledger. Nothing is published yet.

| #   | Task                                                                                                                                                                                                                                        | Size | Depends |
| --- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---- | ------- |
| 2.1 | Registry as the artifact bus: inputs and per-platform outputs as OCI manifests under `<image>/staging`; `desktop.v1` assembly by blob mount; staging retention                                                                              | M    |         |
| 2.2 | `GitHubActionsBuilder`: generated Tier-1 `forge-builder.yml` (per-name secret mapping, `environment:` scoping); dispatch, `run-name` correlation, poll, cancel, timeout; `forge builder workflow --print`; stale-shim check by `forge:hash` | L    |         |
| 2.3 | Signing schemas → packager environment; secret resolution on the builder for `FileSecrets` / `ExternalSecrets`; presence preflight; `forge builder doctor`                                                                                  | M    |         |
| 2.4 | `--release` for desktops: completeness, one-version-one-byte-set refusal, version read and tag consistency warning, variant key, ledger entry, `--plan` coverage                                                                            | M    | 2.1     |
| 2.5 | Builder-side signature verification (codesign/spctl/stapler; Authenticode + publisher CN) recorded in the result doc                                                                                                                        | S    | 2.3     |
| 2.6 | `SSHBuilder` (may slip without blocking anything)                                                                                                                                                                                           | S    | 2.1     |
| 2.7 | forge CI: `forge builder run` exercised on `windows-latest` and `macos-latest`, since the builder is forge running on those OSes                                                                                                            | S    | 1.3     |

**Exit:** from control-plane CI, `forge env build desktop-alpha --release …`
produces signed, notarized installers whose file list, web-bundle digest and
signing verdicts match a `release.yml` run of the same reliant tag.

### Phase 3: publication is a promotion

| #   | Task                                                                                                                                                                                                  | Size | Depends  |
| --- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---- | -------- |
| 3.1 | Object-store layer: `S3Bucket` beside GCS in the shared uploader (also opens `OnBucket` to R2/S3); write-once semantics; per-rule `Cache-Control`                                                     | M    |          |
| 3.2 | `OnDownloads` apply: upload → verify served → feeds (renamed electron-builder bytes; golden test) → purge → wait; idempotent re-apply                                                                 | M    | 3.1      |
| 3.3 | `pkg/release`: `ShapeDesktop`, `SectionDesktop`, four finding codes, `BuildPlan` rules and table tests. **Lands with a control-plane `pkg/release` bump**, because the hosted ledger recomputes plans | M    |          |
| 3.4 | Env kind: an env whose bindings publish to an author-operated store is `self_managed` (also fixes bucket-only frontend envs)                                                                          | S    |          |
| 3.5 | `CloudflareCDN` purge, with names taken from the manifest                                                                                                                                             | S    | 3.2      |
| 3.6 | `forge env status` desktop view: served version per OS, ledger binding, drift                                                                                                                         | S    | 3.2, 3.3 |

**Exit:** `forge env deploy desktop-alpha <rel>` publishes to a **shadow
prefix** on R2, and `--from` moves it to a shadow `latest`. Plans show the new
findings, and approval digests bind.

### Phase 4: announcements, hosted secrets, cutover

| #   | Task                                                                                                                                                                                                     | Size | Depends  |
| --- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---- | -------- |
| 4.1 | `HomebrewCask` announcer: cask rendered from ledger sha256s; opens or updates a PR; never merges                                                                                                         | S    |          |
| 4.2 | `GitHubRelease` announcer, with the tag-ancestry check                                                                                                                                                   | S    |          |
| 4.3 | Hosted build- and publish-scoped grants redeemed with runner OIDC: control-plane grant table, OIDC trust config, `HostedSecrets` resolution on builder and publisher                                     | L    |          |
| 4.4 | Generated two-stage desktop workflow for control-plane (`--plan-only` → environment-protected `--approve`)                                                                                               | S    |          |
| 4.5 | Shadow release: one alpha and one stable through forge in parallel with `release.yml`; diff file sets, hashes of platform-neutral inputs, feed structure, signatures; install-and-update test on each OS | M    | 4.1, 4.2 |
| 4.6 | Cutover: switch production publication to forge; retire everything in §8; update the release skill                                                                                                       | M    | 4.5      |

4.3 is not on the cutover path. Cutover can happen on `ExternalSecrets`, with
4.3 landing afterwards.

## 10. Risks

| Risk                                                                                                                 | Mitigation                                                                                                                                                                                      |
| -------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Feed compatibility.** A forge-written feed that electron-updater misreads breaks every installed app.              | forge does not write feed content. It renames electron-builder's own bytes. A golden test pins that the content is channel-independent. 4.5 runs install-then-update on each OS before cutover. |
| **Notarization latency and flakiness** (minutes to an hour; 403s on agreement changes).                              | Per-job timeouts; retries rebuild only the failed platform (job-digest cache); `notarize-core.js` error classification is kept, because the packager still runs it.                             |
| **GitHub dispatch mechanics**: run correlation, API rate limits, Free-plan concurrency (5 macOS jobs).               | `run-name` correlation by `job_id`; polling with backoff; one release at a time per app (the ledger lock already serializes cuts of one version).                                               |
| **Cost**: moving macOS minutes from public reliant (free) to private control-plane (billed).                         | The builder's `repo` is declared per platform. Builders can stay in public reliant while orchestration runs from control-plane (open question 2).                                               |
| **Secrets in a public repository's builder** (status quo under `ExternalSecrets`).                                   | GitHub environments with required reviewers scope each platform's secrets; phase 4 grants remove stored secrets entirely.                                                                       |
| **Registry storage**: about 2.5 GB per release in GAR.                                                               | Staging retention (days); release-artifact retention follows the ledger (keep what any env binds plus the last N). Roughly $0.25/month per retained release at GAR list price.                  |
| **forge on Windows/macOS runners.** The builder is forge, and Windows legs have broken before on Unix-only syscalls. | 2.7 runs `forge builder run` on both OSes in forge CI. The Windows path work from #479 already applies.                                                                                         |
| **`pkg/release` closed-set additions** need the hosted ledger upgraded in lockstep.                                  | 3.3 ships as one forge release plus one control-plane bump, behind the existing "decode refuses unknown" rule, so an old server refuses loudly instead of misclassifying.                       |
| **Partial-platform releases become impossible.** Some will miss shipping "everything that built."                    | Deliberate. A hole in a release used to be invisible. Dropping a platform is now a reviewable declaration change, and `desktop_platform_dropped` shows what it costs.                           |
| **Scope**: this is about a quarter of work across three repos.                                                       | Every phase stands alone. Phase 1 already removes the unreachable-workload hack and moves the local channel onto forge; phase 2 alone gives reproducible signed builds and a ledger.            |

## 11. Open questions for the owner

1. **Where does the declaration live?** This proposal puts it in control-plane,
   which already owns the endpoints, the ledger and the GitSource pin pattern,
   and builds public reliant at a pinned tag. The alternative is to make
   reliant a forge project (`forge.yaml`, its own ledger). That is cleaner
   ownership, but a second project ledger and a second home for the endpoint
   projection.
2. **Which repository hosts the builders?** Public reliant gives free macOS
   minutes, with signing secrets in its GitHub environments until phase 4.
   Private control-plane is billed, and its secrets sit beside everything else.
   The recommendation is reliant until 4.3 lands, then decide again.
3. **Is storing installers in GAR acceptable** as the immutable release store,
   given about 2.5 GB per release and a second copy on R2?
4. **Refusing partial-platform releases**: confirm that this change in
   behavior is wanted.
5. **Alpha and stable coupling.** Should promoting to `latest` also move
   `alpha` when alpha is behind? The proposal says no. `--from desktop-latest`
   is an explicit step, and `forge env status desktop-alpha` shows the gap. An
   opt-in `OnDownloads.follows = "desktop-latest"` is possible if wanted.
6. **App-side channel selection.** The app derives its channel from its own
   version string. Keep that, or have the build record the channel it was
   installed from? This is reliant-side work, and the forge design works
   either way.
7. **Build secrets and the variant key.** The variant key hashes `build_env`
   only. Two envs with equal endpoints but different Sentry DSNs would share a
   variant key while producing different bytes. Should forge mix an HMAC of
   `build_secrets` values into the key, or forbid differing values within a
   variant?
8. **Release labels.** A desktop cut and a control-plane cut share one label
   namespace. Should desktop releases follow a convention such as
   `desktop-<app version>`, or should forge default the label to that when an
   env declares only desktops?
