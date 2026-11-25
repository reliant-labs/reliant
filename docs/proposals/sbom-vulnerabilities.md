# Proposal: SBOMs and vulnerability findings as a forge default

**Status:** approved by the owner on 2026-11-20 (see
[Decisions](#decisions-owner-2026-11-20)); implementation in progress. Nothing
here has landed in forge, control-plane or reliant yet.
**Date:** 2026-11-20
**Scope:** forge (release cut, ledger, CLI, KCL `vuln_gate`), control-plane
(findings store, rescans, API, promote-time gate), reliant (web pages, the
patch workflow).

## Summary

Every release forge cuts gets an SBOM per artifact, stored with the release
record by digest and scanned against a vulnerability database. The findings
are stored. Deployed releases are rescanned daily, because new CVEs land
against bytes nobody changed. A fix starts as a deterministic dependency-bump
PR, and an agent takes over in the same run if the bump breaks the build or
tests. A forge project gets all of this with no setup, the same way it gets
logs, traces and health probes.

Three decisions are settled and this document does not reopen them:

- **Source of truth.** Each release's SBOM is scanned when the release is
  recorded, the findings are stored, and deployed releases are rescanned daily.
- **Patching.** A deterministic bump PR comes first. If the build or tests
  break, an agent workflow takes over in the same run.
- **Default.** It is a forge feature every project gets on scaffold, with zero
  setup.

The recommended design:

1. **`forge env build --release` (and every deploy that cuts a release)
   generates a CycloneDX SBOM for each artifact.** That covers images (OS and
   language packages, any language, any Dockerfile), host binaries, hosted
   backends and static sites. The generator is syft, run as a pinned and
   checksum-verified tool. The SBOM's **digest is recorded on
   `release.Artifact`** as evidence, not identity. Its **bytes travel with the
   artifact as an OCI 1.1 referrer** in the same registry, using the tag
   fallback where the registry has no referrers API (GHCR, our hosted
   registry). Non-OCI artifacts store their SBOM in the ledger's own storage. A
   Reliant-side copy exists only as a scan cache
   ([1.3](#13-where-the-sbom-is-stored-oci-a-bucket-or-the-ledger)).
2. **The SBOM is scanned with grype at the cut.** The verdict prints there,
   and for a self-hosted env forge evaluates `vuln_gate` before it writes the
   promotion, as it already does for `required_gates`.
3. **An env that records on a control plane uploads each SBOM.** That means
   Reliant hosting, a user cluster with `forge.ControlPlane`, and an external
   CI using `forge release record`. **The control plane scans it itself and is
   authoritative.** It stores findings keyed by org, project, SBOM digest, and
   through the release and promotion tables, by release and environment. It
   rescans every deployed release daily against a mirrored DB, emails org
   admins about new KEV or critical findings, and enforces `vuln_gate` inside
   the promotion transaction.
4. **`reliant forge vuln list|show|scan|db|exempt|patch`**, with `--json` and
   forge's shared exit-code table. `vuln patch` is the deterministic bump per
   ecosystem. It edits, verifies and (optionally) opens the PR.
5. **The KCL `vuln_gate` becomes a real gate under the warn-first rollout.** A
   new row in `lint_rollout_table.go` covers it. While a project's
   `forge_version` is below `GatesFrom`, an `enforce` declaration reports
   "would block; gates from vY" and blocks nothing. The schema default stays
   `audit`.
6. **The web app** gets a Vulnerabilities row in the project nav, hidden until
   there are findings. It also gets severity counts on Overview, a detail page,
   and **Patch** / **Patch with agent**. Both buttons start one built-in Reliant
   workflow, `vuln-patch`. Patch runs the deterministic step first, and Patch
   with agent skips it.

Every change is additive: new fields, RPCs, tables, verbs and a workflow file.
Nothing existing changes meaning (see [Semver](#5-semver-and-backwards-compatibility)).

## What exists today

| Piece                    | State                                                                                                                                                                                                                                                                                                                                            | Where                                                                                                                   |
| ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------- |
| `forge ci vuln-scan`     | It runs govulncheck (v1.7.0) and `npm audit` over the **source tree** as the gate steps `vuln-go` and `vuln-npm`. It passes or fails with exemptions from `ci.vuln_scan.exemptions`, and **stores nothing**                                                                                                                                      | `internal/cli/ci.go:348,512`, `ci_vuln_npm.go`, `internal/cigate/plan.go:470-490`                                       |
| Release cut              | It runs `docker build` and `docker push`, then reads the digest with `imagetools inspect`. Then `cutReleaseFromBuildState` writes `release.Release`, then `Releases.Cut`. A control-plane env goes to `DeployService/CutRelease`; any other env goes to `~/.forge/ledger/releases.jsonl`. **No SBOM, vulnerability or attestation field exists** | `internal/cli/build.go:1750,1892,1988`, `pkg/release/release.go:162-195,347-402`, `internal/cli/hosted_ledger.go:67-78` |
| Build attestations       | Every docker build is forced to `--provenance=false`, because buildx attestations break reproducible digests. No `--sbom` is passed                                                                                                                                                                                                              | `internal/cli/build_source_date_epoch.go:28-35,96-102`                                                                  |
| `forge release record`   | Writes a record-only release and a promotion through `CutRelease` and `Promote`. Images are pinned by digest                                                                                                                                                                                                                                     | `internal/cli/release_record.go:58,494-527`                                                                             |
| README / concepts claims | They promise Trivy scans, an SPDX SBOM, cosign and provenance. These exist **only in the scaffolded GitHub workflow** (`anchore/sbom-action` output saved as a workflow artifact, plus `trivy-action`). forge's Go code does none of it                                                                                                          | `docs/concepts.md:438-440`, `docs/benefits.md:2130-2140`, `internal/templates/ci/github/build-images.yml.tmpl:117-195`  |
| KCL `vuln_gate`          | `VulnPolicy` has these defaults: `mode = audit`, `block_severity = critical`, `warn_severity = high`, `block_kev = True`, `fixable_only = True`, plus time-boxed exemptions. `provenance_sbom` defaults to `enforce`. **The policy is validated and recorded, and nothing evaluates it**                                                         | `kcl/schema.k:6405-6437,6636`, `pkg/release/delivery_policy.go:313`, called only from `bundle.go:1204`                  |
| A gate that does block   | `required_gates` is enforced by the control plane at promote (and by forge for a self-hosted env). `forge obs check --gate-json` feeds it                                                                                                                                                                                                        | `delivery_policy.go:83-93`, `internal/cli/gate_cmd.go:154`                                                              |
| Tool pins                | `internal/templates/toolversions.go`, `forge tools pins`. Every tool is installed with `go install module@version` (trusting Go's sumdb). There is no binary download and no checksum of forge's own                                                                                                                                             | `internal/cli/tools.go:126-512`, `internal/cigate/tools.go:25-296`                                                      |
| control-plane            | Stores **nothing** about SBOMs or vulnerabilities. `identityadvisory` (Zitadel only, no DB) has reusable semantics: unknown is not clean, a missing severity counts as critical, and an acknowledgement expires                                                                                                                                  | `internal/identityadvisory/advisory.go:43-187`                                                                          |
| reliant                  | Has no vulnerability concept. It has workflows with triggers, `RunService.StartRun`, `github/pr.create@1` with server-held credentials, and the `create_worktree` → agent → `git_publish` → PR pattern                                                                                                                                           | `internal/workflow/builtin/autopilot-issue-worker.yaml:268-469`, `proto/reliant/v1/run.proto:40,299`                    |

So the schema is ahead of the code: it declares a gate nothing feeds, and the
docs describe a pipeline that exists only in one optional CI template. This
proposal makes the declaration true.

## 1. The forge default

### 1.1 What an SBOM describes: what ships, per artifact

Every artifact is described by an SBOM of **the bytes that ship**, not of the
repository:

| Artifact                                                                                     | SBOM source                                                                                                                                                                                                                                                     | Why                                                                                                                |
| -------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------ |
| Image forge built (`build.type` go, docker, shell; adopted Dockerfile apps; hosted backends) | The built image, read from the local daemon right after the build and recorded against the pushed digest. Syft catalogs OS packages (deb, rpm, apk) plus language packages in the layers: Go buildinfo, node_modules, site-packages, jars, gems, cargo binaries | It covers every language and every Dockerfile. The base image's OS packages are where most container CVEs are      |
| Prebuilt image (`spec.image` pinned by digest)                                               | The image in the registry, read by digest with the user's docker credential helpers                                                                                                                                                                             | forge did not build it, but it ships                                                                               |
| Host workload (a Go binary under `forge.OnHost`)                                             | The built binary (Go buildinfo lists every module and the toolchain version)                                                                                                                                                                                    | The binary is the artifact. go.mod lists modules that may not be linked                                            |
| Host workload in another language (node, python)                                             | The workload directory's lockfiles                                                                                                                                                                                                                              | No image exists to read                                                                                            |
| Static site (hosted, bucket, Firebase)                                                       | The frontend's lockfile, with dev dependencies excluded                                                                                                                                                                                                         | A bundle has no package metadata. The lockfile's production dependencies are the best evidence of what was bundled |
| npm / gomod / file artifacts                                                                 | Their own manifest or lockfile                                                                                                                                                                                                                                  | Same rule                                                                                                          |

An SBOM failure **never** fails a build during the rollout release. The cut
warns `release v… recorded without an SBOM for <artifact>: <why>`, and the
release records `sbom: {status: "unavailable", reason}`, which shows up later
as "not scanned", never as "clean". After `GatesFrom`, `provenance_sbom =
enforce` (already the default) refuses a cut that has an artifact without an
SBOM. That is the existing declaration taking effect. The provenance half of
that control stays recorded-only until signing work makes it real. The
delivery-policy skill's status table is updated to say exactly that.

Generation runs concurrently per artifact. It is cached by artifact digest in
`.forge/cache/sbom/<digest>.cdx.json`, so a re-cut or a no-version deploy that
reuses a release costs nothing. `--no-sbom` on `env build` / `env deploy`
skips it for one run, with a warning. `provenance_sbom = forge.Control {mode =
"off"}` turns it off for an env declaratively. The KCL already refuses `off`
with an enforcing `vuln_gate`.

**Format.** CycloneDX 1.6 JSON is the stored, canonical form, because it has
first-class vulnerability and VEX fields, which a later "not affected"
statement needs. SPDX 2.3 JSON is accepted as input on `release record
--sbom`, converted on ingest with the original kept, and offered as an export
(`forge vuln sbom <env> --format spdx`). This is what the README's "SPDX SBOM"
promise becomes.

### 1.2 Generator and scanner: a pinned tool or a Go library

There are two separable choices. Which scanner and database (Decision 2), and
how forge delivers the scanner (Decision 3).

**Scanner and DB options:**

|                                     | syft + grype (Anchore)                                                                                                                                                                                           | osv-scanner v2 (+ osv-scalibr)                                                                              | Trivy                                                                       |
| ----------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------- |
| SBOM                                | syft: the widest cataloger set (OS plus about 20 language ecosystems, binaries)                                                                                                                                  | scalibr extracts. SBOM output is secondary                                                                  | Built in, CycloneDX/SPDX                                                    |
| DB                                  | One SQLite archive built from NVD, GHSA, distro feeds (Debian, Ubuntu, RHEL, Alpine, Wolfi, ...) and OSV, with KEV and EPSS enrichment (verify on the pinned version in phase 0). Versioned listing, checksummed | OSV.dev, the open schema. Offline mode uses per-ecosystem zip exports. Distro coverage is newer and thinner | Its own DB, distributed as an OCI artifact from ghcr; a large single binary |
| Airgapped                           | `grype db import <file>`. The file is a plain download that can be mirrored                                                                                                                                      | Download the ecosystem zips                                                                                 | Mirror the DB OCI artifact                                                  |
| Install                             | Both are Go modules, and release binaries come with checksums and cosign signatures                                                                                                                              | Go module, release binaries                                                                                 | Release binaries. `go install` is unsupported in practice                   |
| Matches the existing KCL vocabulary | `vuln_gate` already says "Grype over the SBOM"                                                                                                                                                                   | Needs a severity mapping (OSV carries CVSS vectors, not a severity label)                                   | Yes                                                                         |

**Recommendation: syft + grype.** Distro coverage is mature (this matters most
for adopted Dockerfile apps), there is one DB file to mirror and import
offline, the formats match end to end, and the KCL already names grype. OSV
becomes a source inside grype's DB, not a second scanner. govulncheck stays a
dev-loop gate (`forge ci vuln-scan`), and its reachability data ("called / not
called") can enrich findings later.

**Delivery options:**

|                               | Go library embedded in forge                                                                                                                                               | Pinned release binary, sha256 in forge source                                                                | `go install` (today's mechanism)                                        |
| ----------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------- |
| Zero setup in any language    | Yes                                                                                                                                                                        | Yes                                                                                                          | **No.** Needs a Go toolchain, which an adopted Python app's CI may lack |
| forge binary and dependencies | It grows a lot: stereoscope, containerd and registry clients, many cataloger dependencies. Every dependency passes forge's license allowlist (`scripts/check-licenses.sh`) | Unchanged                                                                                                    | Unchanged                                                               |
| Scanner fix cadence           | Ships only with a forge release                                                                                                                                            | Pin bump in `toolversions.go`. Overridable per project (`vuln.tools.grype: vX` in forge.yaml)                | Same as binary                                                          |
| Verification                  | Go sumdb                                                                                                                                                                   | sha256 pinned in forge's source per OS and arch, plus optional cosign verification of the upstream signature | Go sumdb                                                                |
| control-plane                 | Imports the same library                                                                                                                                                   | Ships the same pinned binary in its worker image (control-plane pins forge, so it gets the same pin)         | n/a                                                                     |

**Recommendation: a pinned release binary.** It adds a small general mechanism,
`internal/toolfetch`: download into `~/.forge/tools/<name>/<version>/`,
verify the sha256 compiled into forge, then atomically rename. `forge tools
pins` and `forge doctor` report it. The same mechanism can later serve any
non-Go tool. If the platform has no published binary, forge falls back to `go
install` when a Go toolchain is present, and otherwise reports the tool
missing. Missing means "not scanned", never clean.

**The DB.** `~/.forge/cache/vulndb/` holds the current grype DB, refreshed when
it is older than 24h. `FORGE_VULN_DB_URL` points at a mirror, and `forge vuln
db import <file>` loads one offline. Every scan records `{scanner, version,
db_schema, db_built_at}`, so a verdict always says how fresh its data was. A DB
older than 5 days makes the verdict `stale` (exit 2, "could not determine").
It is not a pass.

### 1.3 Where the SBOM is stored: OCI, a bucket, or the ledger

The owner asked whether the SBOM should travel in the OCI registry or live
somewhere else, such as an internal forge/Reliant bucket. This section answers
that. It is Decision 1.

**Separate three things.** An SBOM has an _identity_, _bytes_, and _derived
data_, and each belongs in a different place:

| What                                                                                                                 | Where it lives                                                                                                                                                                                   | Why                                                                                                                                                                                                                                     |
| -------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Identity**: the SBOM's digest, bound to `(release, artifact)`                                                      | **The release record** (`release.Artifact.SBOM`), in whichever ledger the env uses: machine `releases.jsonl` or control-plane `deploy_release_artifacts`                                         | The ledger is already the index of what a release _is_. Binding by digest makes the SBOM verifiable wherever its bytes sit, and correctness never depends on a registry's referrers query or its GC                                     |
| **Bytes**, for anything that is an OCI artifact: images, hosted static sites, bundles                                | **Next to the artifact, in the same registry, as an OCI 1.1 referrer** (`subject` = the artifact's digest), with the OCI referrers _tag-schema fallback_ where the registry has no referrers API | It travels with the image across registries, mirrors and promotions (`crane cp` / `oras cp -r` carry referrers), the user's existing access control and retention apply, and a user cluster or self-hosted env needs **no new storage** |
| **Bytes**, for anything that is not OCI: host binaries, npm, gomod and file artifacts, version-only `release record` | **The ledger's own storage**: `~/.forge/ledger/sboms/` for the machine ledger, control-plane object storage under an org prefix for a control-plane ledger                                       | There is no registry to attach to. A host-only env may not have a registry at all                                                                                                                                                       |
| **Findings** (scan results, first-seen, verdicts)                                                                    | **Derived**, never stored with the artifact. control-plane's DB on a control-plane env. `.forge/cache` and `~/.forge/ledger/findings/` locally, recomputed on read when the DB is newer          | A finding changes daily against unchanged bytes. Attaching findings to the image would be a mutable fact on an immutable artifact                                                                                                       |
| **A Reliant-side copy** of every SBOM the control plane scans                                                        | control-plane object storage, keyed `(org, sbom digest)`                                                                                                                                         | A **cache**, verified by digest, never the source of truth. The rescan job must not depend on pull credentials into a user's private GHCR, and must not re-pull thousands of referrers a day                                            |

**What was checked (2026-11-20).** Each registry was probed anonymously with
`GET /v2/<repo>/referrers/<unknown digest>`. A compliant registry answers 200
with an empty index.

| Registry                                                                                                 | Referrers API                                                                                         | Consequence                                                                                         |
| -------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------- |
| Google Artifact Registry                                                                                 | **yes**. It returns `"manifests": null` where the spec says `[]`, which forge's reader must tolerate  | Native referrer                                                                                     |
| Docker Hub                                                                                               | **yes**                                                                                               | Native                                                                                              |
| Amazon ECR (public probed)                                                                               | **yes**                                                                                               | Native. Private ECR to be confirmed in phase 0                                                      |
| Azure (MCR probed; ACR not)                                                                              | likely yes                                                                                            | Confirm in phase 0                                                                                  |
| GHCR                                                                                                     | **no** (404)                                                                                          | Tag fallback (`sha256-<hex>` index tag)                                                             |
| quay.io                                                                                                  | no (400)                                                                                              | Tag fallback                                                                                        |
| **Reliant hosted registry** (`registry.reliantapi.com`, CNCF distribution v3.1.2 + GCS + our `sigguard`) | **no**: distribution v3.1.2 has none (`control-plane/tools/registry-reliant/sigguard/guard.go:33-36`) | Tag fallback. The manifest with `subject` is stored, and the fallback tag is what makes it findable |
| Harbor, zot                                                                                              | not probed (documented as supported in recent versions)                                               | Probed in phase 0                                                                                   |

**What exists to build on:**

- **forge already pushes OCI artifacts with oras-go v2.7.0:** hosted static
  sites (`application/vnd.forge.staticsite.v1`, `internal/deploytarget/staticsite_oci.go:53-56`)
  and bundles (`pkg/release/bundle.go:35`). oras-go implements referrers _with_
  the tag-schema fallback. Attaching an SBOM therefore needs no new dependency,
  and no cosign.
- **forge signs nothing today.** There is no cosign or sigstore in its Go code.
  `env build` forces `--provenance=false` to keep digests reproducible, and the
  only cosign use is the scaffolded GitHub workflow. `release.Provenance`
  records repo, commit, branch and author (`pkg/release/provenance.go:32-52`).
  It is git provenance, not a signed statement.
- **control-plane already signs, cosign-compatibly, without the cosign
  library.** Its delivery signer publishes `sha256-<hex>.sig` tags signed with
  a per-org OpenBao transit key (`internal/deliverysign/artifact.go:18-87`,
  `internal/workers/delivery_signature_publisher/`), and `tools/cosigncompat`
  pins the compatibility.
- **Hosted static sites are OCI artifacts too.** The staticsite operator copies
  only the site layer into the serving bucket
  (`sites/<tenant>/<site>/releases/<digest>/`). The release itself, and its
  referrers, stay in the registry. So a site's SBOM attaches exactly like an
  image's, and needs no bucket path of its own.

**How the lean holds up.** The proposed design was: a signed attestation as an
OCI referrer, non-image SBOMs beside the artifact in the hosting bucket,
findings as derived data, and a Reliant bucket only as a cache. **Adopt it,
with four amendments:**

1. **Storage and signing are separate steps; do not make storage wait for
   signing.** forge attaches a _plain_ referrer:

   - `artifactType: application/vnd.cyclonedx+json`, one layer holding the SBOM,
     `subject` = the artifact digest.
   - Annotations: `org.opencontainers.image.created`, `dev.forge.sbom.generator`,
     `dev.forge.release`.

   A signature over _that referrer's digest_ is added by whoever can sign:

   - On a control-plane env, the delivery signer signs it with the org key,
     the same way it signs bundles today. This becomes a third thing it
     publishes.
   - On a self-hosted env, nothing signs it until forge's image signing lands
     (delivery-policy F15).
   - Whoever wants a cosign in-toto attestation (`cosign attest --type
cyclonedx`) can still add one beside it, and forge reads either.

   Two reasons to keep them apart:

   - Making "signed" a precondition of "stored" would leave every self-hosted
     project with no SBOM until signing ships.
   - Rekor's default 100 KiB attestation limit means a real SBOM cannot go
     through public keyless signing by default anyway.

2. **Non-OCI SBOMs do not go "next to the artifact in the hosting bucket".**
   Two reasons:

   - The only bucket-hosted release kind, static sites, is already an OCI
     artifact (above). The bucket holds a derived serving copy that the
     operator rewrites.
   - The artifacts that truly are not OCI (host binaries, npm, gomod, file)
     have no hosting bucket at all.

   They go in the ledger's own storage instead, which every env has.

3. **The release record carries the digest, whatever the registry does.**
   GHCR, quay and our own hosted registry have no referrers query, and
   referrers are the first thing a careless GC drops. So a lookup goes
   ledger → SBOM digest → fetch: the referrer by digest, else the ledger copy,
   else the control-plane cache. It never goes "ask the registry what is
   attached".
4. **The Reliant-side copy is mandatory for scanning, but it stays a cache.**
   - For a user registry the control plane has no pull credentials, so forge
     uploads the same bytes (`RecordReleaseSBOM`). The control plane keeps them,
     verified against the digest the release record names.
   - For the hosted registry the control plane can always re-read the referrer.
   - Either way the copy can be dropped and rebuilt. Retention keeps it while a
     retained release references it.

**What the hosted registry needs** (control-plane task C7):

- `sigguard` already lets tenant _image_ repos take referrers and `sha256-*`
  tags. It refuses them only in `bundle.v1` repos, and SBOMs are not attached
  to bundles.
- **Retention must keep a kept artifact's referrers and fallback tags.**
  Retention follows `registry_manifests.subject_digest`, documented as the
  _parent index_ link (`internal/ociregistry/retention.go:73-75,140-151`).
  Whether the inventory records an OCI `subject` is unconfirmed. If it does,
  retention also follows the link _upward_, and a kept SBOM would pin its image.
  So C7 records referrer links separately (`referrer_of`) and keeps them in one
  direction only: a kept artifact keeps its referrers, never the reverse.

**Attachment by runtime:**

| Env                                    | SBOM bytes                                                                                                          | Notes                                                                                                                                    |
| -------------------------------------- | ------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| Reliant hosting                        | Referrer in the hosted registry (tag fallback) + control-plane copy                                                 | Signed by the delivery signer                                                                                                            |
| User cluster / compose with a registry | Referrer in the user's registry (native or tag fallback) + control-plane copy if the env records on a control plane | `vuln.attach_sbom: false` in forge.yaml turns attachment off for a registry that refuses unknown artifact types, leaving the ledger copy |
| Host workloads, no registry            | Ledger storage                                                                                                      |                                                                                                                                          |
| `forge release record` (external CI)   | Referrer on each `--image` digest when the CI token can push, else upload only                                      | A CI that attached its own SBOM (cosign or `docker buildx --sbom`): `--sbom-from-registry` reads it instead of generating                |

`pkg/release` gains an **evidence** field on `Artifact`:

```go
// SBOM is evidence about the artifact, not part of its identity: a re-cut of
// the same digests with a newer syft is the same release, never a conflict.
SBOM *SBOMRef `json:"sbom,omitempty"`

type SBOMRef struct {
    Digest    string `json:"digest,omitempty"`     // sha256 of the SBOM bytes (the identity)
    Format    string `json:"format,omitempty"`     // "cyclonedx-json@1.6"
    Generator string `json:"generator,omitempty"`  // "syft@v1.x.y"
    Source    string `json:"source,omitempty"`     // image | registry | binary | lockfile | ci-provided
    Referrer  string `json:"referrer,omitempty"`   // repo@sha256:… of the referrer manifest, when attached
    Status    string `json:"status,omitempty"`     // ok | unavailable
    Reason    string `json:"reason,omitempty"`
}
```

The identity check in the cut ("re-cutting the same artifacts is a no-op;
different artifacts are refused") ignores `SBOM`. When a re-cut has an SBOM and
the record has none, the SBOM is backfilled. It never replaces an existing one.

Recording, per ledger:

- **Machine ledger:** `~/.forge/ledger/sboms/sha256-<hex>.cdx.json` is always
  written, even when a referrer was also attached, so a scan never needs the
  network for bytes. Each scan writes `findings/<sbom-digest>/<db_built_at>.json`.
- **Control-plane ledger:** after `CutRelease`, forge calls
  `VulnerabilityService/RecordReleaseSBOM` with the bytes and the referrer
  reference. The server re-derives the digest and binds it to `(release,
artifact_name)`, following the `RecordBundle` precedent. A control plane
  without the RPC answers `Unimplemented`. forge prints one note ("this control
  plane does not store SBOMs yet") and continues, with the referrer and the
  machine copy intact. The capability is advertised (`vuln-findings`), so the
  plan can say it up front.

### 1.4 Scanning at the cut

Right after the SBOMs are written, forge runs grype over each one and prints a
summary under the release line:

```
cut release v1.7.0 (3 artifacts, 3 SBOMs)
vulnerabilities: 1 critical (fixable, KEV), 4 high, 11 medium · grype v0.9x db 2026-11-20
  vuln_gate (prod, enforce): would block: CVE-2026-1234 in golang.org/x/net v0.30.0 (fixed 0.33.0)
  → reliant forge vuln show prod CVE-2026-1234 · reliant forge vuln patch prod
```

For a **control-plane env**, the local result is only advisory. The server
scans the same SBOM and is authoritative, both for the gate and for what the
web shows (Decision 4). For a **self-hosted env**, the local result is the
record, and forge enforces the gate itself before it writes the promotion, the
same split `required_gates` already uses.

### 1.5 Every runtime, and apps forge never built

| Situation                                                                       | SBOM                                                                                                                                    | Initial scan                                     | Daily rescan                                                                                                                                                                                                                                                      | Gate enforced by                                                                                                                              |
| ------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| Reliant hosting (`OnHosted` workloads, hosted static sites)                     | At the cut                                                                                                                              | forge (advisory) + control plane (authoritative) | control plane                                                                                                                                                                                                                                                     | control plane, at promote                                                                                                                     |
| User cluster / compose / host **with** `forge.ControlPlane`                     | At the cut                                                                                                                              | Same                                             | control plane                                                                                                                                                                                                                                                     | control plane (an env it converges) or forge (an env whose apply forge does: "converges nothing") using the control plane's findings          |
| User cluster / compose / host on the **machine ledger**                         | At the cut                                                                                                                              | forge                                            | Rescan on read: `forge vuln list` rescans when the DB is newer than the last scan. The scaffolded CI template gets a nightly `forge vuln scan --deployed <env>` job, which reads running digests the way `forge env status` does and scans them from the registry | forge, before the promotion write                                                                                                             |
| Adopted app, any language, deployed by forge from its Dockerfile (`--kind app`) | The image SBOM at the cut                                                                                                               | As its ledger                                    | As its ledger                                                                                                                                                                                                                                                     | As its ledger                                                                                                                                 |
| Adopted app deployed by its **own CI** (`forge release record`)                 | Generated from each `--image` digest (registry read) unless `--no-sbom`. Or the CI's own `--sbom <workload>=<file>` (CycloneDX or SPDX) | control plane                                    | control plane                                                                                                                                                                                                                                                     | Nothing blocks, because a record-only release is a record. Findings and alerts still apply. The gate verdict is recorded as `audit`           |
| Claimable deploys                                                               | As hosted                                                                                                                               | As hosted                                        | As hosted                                                                                                                                                                                                                                                         | As hosted. Findings belong to the anonymous org, move with it on claim and cascade away on purge (`deploy_releases.org_id ON DELETE CASCADE`) |

### 1.6 `forge release record` for external CI

New flags only:

```
--sbom <workload>=<path>   an SBOM your pipeline already produced (CycloneDX or SPDX JSON); repeatable
--no-sbom                  record without SBOMs (findings show "not scanned")
--sbom-source <dir>        version-only records: describe this source tree's lockfiles instead
```

The default with `--image` pairs is to generate the SBOM from each digest,
using the CI's registry credentials, which a deploying CI has. A failure warns,
and the record still succeeds. The `--json` result gains `sboms: [{workload,
digest, status}]`. The deploy/record skill's recipes add nothing: the zero-flag
path gets SBOMs.

### 1.7 CLI

The new `vuln` group is registered in `internal/cli/root.go` beside `release`
and `obs`:

```
reliant forge vuln list <env> [--release v] [--severity low|medium|high|critical]
                              [--fixable] [--kev] [--include-exempt] [--json]
reliant forge vuln show <env> <advisory-id | package> [--json]
reliant forge vuln scan [<dir> | --image <ref@digest> | --release <v> | --deployed <env>] [--json]
reliant forge vuln sbom <env> [--release v] [--artifact name] [--format cyclonedx|spdx]
reliant forge vuln db status|pull|import <file>
reliant forge vuln exempt <env> <advisory-id> --reason "<why>" --expires YYYY-MM-DD
reliant forge vuln patch <env> [<advisory-id>...] [--ecosystem go|npm|...] [--allow-major]
                               [--verify[=<cmd>]] [--pr | --no-pr] [--branch <name>] [--json]
```

- `list` / `show` read from the control plane when the env records there, and
  from the machine ledger otherwise. The text output ends with the console link
  (`https://forge.reliantlabs.io/forge/p/<project>/vulnerabilities`).
- `exempt` writes a `forge.VulnExemption` into the env's `main.k`, or into its
  env document. Exemptions stay declarative and reviewable, and the vocabulary
  is the one `ci.vuln_scan.exemptions` already uses.
- **Exit codes** use the shared table (`internal/cli/exitcodes.go`):

| Code | `vuln list` / `scan`                                                                                                    | `vuln patch`                                                                                                                  |
| ---- | ----------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| 0    | No finding the env's `vuln_gate` would block                                                                            | Every requested finding bumped and verified                                                                                   |
| 1    | At least one blocking finding (in any mode: the exit says what it found; the mode decides whether a promote is refused) | Bumped, but verification failed: the agent takeover signal                                                                    |
| 2    | Could not determine: an artifact without an SBOM, scanner missing, DB stale                                             | Nothing deterministic to do: an OS package with no base-image route, an ambiguous manifest, or an ecosystem without a patcher |
| 4    | Refused (not signed in for a control-plane env, no `vuln:read`)                                                         | Refused: dirty tree, or a major bump without `--allow-major`                                                                  |

- `--json` prints a versioned document (`schema: forge.dev/vuln-list/v1`):
  `{env, release, coverage: {artifacts, with_sbom}, db: {scanner, version,
built_at, stale}, gate: {mode, graded_as, verdict, blocking: [ids]},
counts: {critical, high, medium, low, unknown}, findings: [{id, aliases,
severity, cvss, kev, epss, package: {name, version, purl, type}, fixed_in,
artifacts, first_seen, exemption}]}`. Its JSON Schema is published like the
  env document's (`forge project schema vuln-list`).

### 1.8 Making `vuln_gate` a real gate, warn first

Where it is evaluated:

1. **At the cut (forge):** advisory only. It prints `would block` with the
   reason and the fix commands.
2. **At promote:**
   - **Control-plane env:** the control plane evaluates the policy recorded in
     the release shape against its own findings for the release's SBOM digests,
     inside the promotion transaction (where `health_gate` runs). It records a
     `vuln_gate` gate on the promotion with the verdict and the blocking ids.
     Refusal reasons: `vuln_gate_blocked` (exit 4, naming the advisories) and
     `vuln_gate_unscanned` (exit 4: an artifact without an SBOM or scan while
     `enforce`). The capability is advertised as `enforces-vuln-gate`.
   - **Self-hosted env:** forge evaluates before the promotion write, exactly
     where it enforces `required_gates` for self-hosted envs today.
3. **Override.** `--override-vuln-gate "<why>"` overrides this gate for one
   promotion. The gate records who, why and which findings. The durable answer
   is still an exemption in KCL.

The rule, unchanged from the schema: block KEV-listed advisories (`block_kev`),
and block at or above `block_severity` when a fix exists (`fixable_only`). Warn
at or above `warn_severity`. Unexpired exemptions match by id or alias (a CVE
exemption covers the GHSA for the same advisory). A missing severity counts as
critical, following `identityadvisory`'s fail-closed rule.

**Warn-first rollout.** Today an `enforce` declaration blocks nothing, so
anyone who wrote one did not opt in to being blocked. The gate is graded with
the lint rollout mechanism:

- A row `{ID: "vuln-gate", IntroducedIn: "v1.N", GatesFrom: "v1.N+1"}` goes in
  `internal/cli/lint/lint_rollout_table.go`. A second row, `sbom-required`, does
  the same for the `provenance_sbom` refusal.
  `TestEveryGradableRuleDeclaresItsRollout` then covers both.
- While the project's `forge_version` is below `GatesFrom`, `enforce` is graded
  as `audit`. The cut, the plan and the promotion gate all say `would block (new
in v1.N; gates from v1.N+1)`.
- `lint.rules: {vuln-gate: error}` opts in early, and `{vuln-gate: warn}`
  extends the grace period, the same vocabulary as every other rollout.
- The control plane grades the same way. The release shape records the forge
  version that cut it, so a release cut by an older forge is never blocked by a
  rule that forge did not know.
- A new gate step, `vuln-sbom`, joins `forge ci`: it scans the source tree's
  lockfiles in `task ci`, through the same rollout row. The existing `vuln-go`
  and `vuln-npm` steps keep their meaning.

The **schema default stays `audit`** (Decision 5). A project scaffolded today
therefore records findings, alerts and shows them, and blocks nothing until
one line opts in.

## 2. The Reliant hosting side (control-plane)

### 2.1 Findings store

The new migrations are expand-only:

```sql
-- One stored SBOM per (org, digest). Deduplication is per org, never across
-- orgs: a cross-org content address would reveal that another tenant ships
-- the same bytes.
vuln_sboms(org_id, digest, format, generator, source, size_bytes, storage_key,
           referrer, created_at, PRIMARY KEY (org_id, digest))

-- Which artifact of which release an SBOM describes (nullable column instead
-- would also work; a join table keeps 00070 untouched).
deploy_release_artifact_sboms(release_id, artifact_name, org_id, sbom_digest,
           PRIMARY KEY (release_id, artifact_name))

-- One row per scan of one SBOM with one DB.
vuln_scans(id, org_id, sbom_digest, scanner, scanner_version, db_built_at,
           status ok|failed, error, started_at, finished_at,
           UNIQUE (org_id, sbom_digest, db_built_at))

-- What a scan found. Replaced per scan (current = the newest ok scan).
vuln_findings(org_id, scan_id, sbom_digest, advisory_id, aliases text[],
           package_name, package_type, package_version, purl, location,
           severity, cvss_score, kev bool, epss real, fixed_versions text[],
           fix_state fixed|not_fixed|wont_fix|unknown,
           PRIMARY KEY (scan_id, advisory_id, purl))

-- First-seen and resolution history, per (project, environment, advisory, package).
-- Maintained by the rescan pass over deployed releases.
vuln_observations(org_id, project, environment_id, advisory_id, purl,
           first_seen_at, last_seen_at, resolved_at, first_release_id,
           PRIMARY KEY (org_id, project, environment_id, advisory_id, purl))
```

The user-facing key the brief asks for, _org, app, release digest,
environment_, falls out of the joins without denormalising:

- A **release's** findings are the findings of the SBOM digests of its
  artifacts.
- An **environment's** findings are those of the release in its newest
  `deploy_promotions` row. That is the same rule `internal/deploystore/live.go`
  uses. `current_promotion_id` is deliberately not read.
- **First seen** comes from `vuln_observations`, per env. "New in prod this
  week" and "fixed in prod since v1.8" are queries over it.

`vuln_sboms.storage_key` points at the control-plane **copy** in platform
object storage under an org prefix (`sboms/<org>/<digest>`, gzip). It is a scan
cache (see 1.3): the canonical bytes are the referrer next to the artifact,
and the ledger storage for non-OCI artifacts. `vuln_sboms.referrer` records
where that referrer is. The copy is retained while any release still
references it, by the same keep-set rule `registry_retention` uses, and
cascades with the org. Upload limits are 32 MiB compressed and 200k
components. Anything larger is refused with a reason, never truncated.

### 2.2 Scanning on ingest, and the daily rescan

- **Ingest.** `RecordReleaseSBOM` stores the bytes and returns. A
  `vuln_scan` worker (a forge worker on `workerloop`) claims unscanned digests
  and runs the pinned grype **in a subprocess with CPU, memory and time
  limits**. It never runs in the API process, because an SBOM is untrusted
  input. Its findings are written in one transaction.
- **DB mirror.** A `vuln_db_sync` worker runs daily. It downloads grype's DB
  listing, verifies the checksum, and stores the archive in the platform
  bucket. Scanners only read the mirror, so a CDN outage cannot fail a scan,
  and every pod in a pass uses the same DB.
- **Daily rescan.** A `vuln_rescan` worker uses `workerloop.Every(24h)` with
  the splay `registry_retention` uses (`internal/workers/registry_retention/worker.go:56,131`):
  1. Select the distinct SBOM digests of every environment's current release,
     plus the releases promoted in the last 30 days, which are the rollback and
     roll-forward targets.
  2. Rescan any digest whose newest scan predates the current DB. Scans are
     idempotent on `(org, digest, db_built_at)`.
  3. Diff the result against `vuln_observations`: insert new findings, bump
     `last_seen_at`, and stamp `resolved_at`.
  4. For a **new** KEV finding, or a fixable critical, in a deployed env:
     `OrgNotifyInternalService.NotifyOrgAdmins`, once per `(org, kind=vuln,
subject=project/env, incident=advisory)`. Alert receivers (Slack, in-app)
     come in a later phase through the alerting service.
  5. Fairness: per-org concurrency caps and bounded batches. A huge org cannot
     starve the pass.
- **Unknown is not clean.** A scan failure leaves the previous findings in
  place and marks the env `scan_failed` with a time. The API and UI show "last
  scanned 3 days ago (failed since)". Zero findings are never invented.

### 2.3 API for the web app and the CLI

A new service in package `controlplane.v1`,
`proto/services/vulnerability/v1/vulnerability.proto`. The web reaches every
`controlplane.v1.*` service directly on platform-api (`reliant/frontends/web/src/lib/connect.ts`),
so it needs no proxy wiring:

```proto
service VulnerabilityService {
  // Writes (forge CLI, deploy:write).
  rpc RecordReleaseSBOM(RecordReleaseSBOMRequest) returns (RecordReleaseSBOMResponse);

  // Reads (vuln:read).
  rpc GetVulnerabilitySummary(GetVulnerabilitySummaryRequest) returns (GetVulnerabilitySummaryResponse);
  //   per env: counts by severity, kev count, fixable count, coverage, last_scanned_at, scan_state
  rpc ListVulnerabilities(ListVulnerabilitiesRequest) returns (ListVulnerabilitiesResponse);
  //   filters (all optional): environment, release, min_severity, fixable, kev, include_exempt; paged
  rpc GetVulnerability(GetVulnerabilityRequest) returns (GetVulnerabilityResponse);
  //   one advisory in one project: description, references, every affected package/artifact/release/env,
  //   observations (first seen, resolved), exemption state per env, gate verdicts
  rpc GetReleaseVulnerabilities(GetReleaseVulnerabilitiesRequest) returns (GetReleaseVulnerabilitiesResponse);
  //   what forge vuln list --release and the plan read
}
```

`vuln_admin` gets `RescanNow` and `GetVulnDBStatus` for `control-plane ops
call`. Promote's gate evaluation is internal to the deploy handler. It reads
the store and needs no RPC.

### 2.4 Tenancy and grants (fails closed)

- Every handler resolves the org with the existing `callerOrgID` family
  (`internal/handlers/deploy/helpers.go:247-433`), and every query is scoped by
  that org. A project or environment that is not the caller's org's is
  `NotFound`, so existence is not revealed.
- **A new scope, `vuln:read`**. It is added to forge's
  `pkg/accesstoken` and re-exported by `internal/accesstoken/scope.go`. The
  `access_tokens` CHECK is widened in an expand migration, and the scope joins
  the Admin preset. A one-time grant gives it to every member who holds
  `deploy:read` today, so no one loses sight of their own project. Machine
  tokens carry it only when it was minted in. A list of exploitable
  vulnerabilities is an attack map, so a CI token that can record deploys does
  not get to read it by default.
- **Env-bound** grants and tokens see only their env (`callerOrgIDForEnvRead`).
- `RecordReleaseSBOM` needs `deploy:write` on the release's env, the same as
  `CutRelease`.
- An operation with no scope mapping is refused for machine principals, which
  is the existing fail-closed rule. Grants are re-read on every request.
- Exemptions are never written through the API. They are KCL, through a PR or
  `forge vuln exempt`. The API reads them from the recorded shape.

## 3. The Reliant web side

All of it reads `VulnerabilityService` through the TanStack Query hook pattern
in `frontends/web/src/hooks/observability-queries.ts`. The TypeScript stubs
come from reliant's vendored control-plane proto (`buf.gen.controlplane.yaml`,
synced by `task generate:controlplane`), so the web work starts once the proto
PR has merged and been synced.

- **Nav row.** "Vulnerabilities" goes under **Issues** in
  `components/Forge/Console/groupedNav.tsx` (and in the flat
  `ConsoleShell.tsx` list). It follows the Profiles pattern
  (`groupedNav.tsx:112`): hidden on a _definite_ empty read
  (`GetVulnerabilitySummary` says no findings and full coverage), shown while
  loading or on error, and always shown when it is the open page. The badge is
  the count of blocking findings (KEV plus fixable at or above the env's block
  severity) in prod, else the highest env. A project with releases that have no
  SBOM yet shows the row with "not scanned", because unknown is not clean.
- **Overview.** A `VulnerabilitiesCard` beside `UsersCard` in
  `components/Forge/Workloads/OverviewHome.tsx`. It shows C/H/M/L counts per
  environment, a KEV chip, "N fixable", the last scan time, and one link.
  Deployed-app cards get a small severity dot.
- **Detail page** (`/forge/p/<project>/vulnerabilities`):
  - A table grouped by advisory: severity, KEV, EPSS, package, installed →
    fixed, envs affected, first seen, exemption state.
  - Filters: env, severity, fixable, KEV, exempt.
  - Row selection drives the actions.
  - The advisory drawer or page (`/vulnerabilities/<id>`) shows the summary,
    references, affected artifacts and releases, the history (first seen,
    resolved), and gate verdicts on recent promotions.
- **Actions** (on a selection, or on one advisory):
  - **Patch** starts the `vuln-patch` workflow with `mode: deterministic`. The
    deterministic bump runs first and the agent takes over if verification
    fails, which is the settled flow.
  - **Patch with agent** starts it with `mode: agent`, which skips the bump.
  - Both call `RunService.StartRun` (`proto/reliant/v1/run.proto:40,299`) with
    `{project, env, advisories, repo}` and link to the run, and then to the PR.
  - The buttons are disabled with a stated reason when the project has no
    linked repo (`Project.forge_project_name` ↔ `Repo.forge_apps`), no GitHub
    connection, or when the selection has no fix.
  - Next to them, "Exempt…" opens the same PR flow with a `VulnExemption` edit
    instead of a bump (phase 4).

## 4. The patch flow

### 4.1 `reliant forge vuln patch`: the deterministic bump

The input is the findings for the env's current release, or an explicit list
of advisories. The patcher works in four steps.

1. **Resolve each finding to a manifest in the repo.** A source-tree SBOM
   carries the path. For an image SBOM, the patcher maps the package to the
   repo:

   - Go buildinfo leads to the main module's `go.mod`.
   - An npm or pip package is found by searching the repo's lockfiles for that
     name.
   - OS packages go to the Dockerfile's `FROM`.

   When the mapping is ambiguous, the finding is left to the agent, with the
   reason.

2. **Choose the target.** The target is the lowest fixed version at or above
   the installed one, staying in the installed major unless `--allow-major` is
   given. A major-only fix is reported as needing review, and it is the agent's
   job.
3. **Apply the bump with the ecosystem's own tool**, so lockfiles stay
   consistent:

| Ecosystem                    | Direct dependency                                                                                                         | Transitive dependency                           |
| ---------------------------- | ------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------- |
| Go                           | `go get mod@vX && go mod tidy` in each module (go.work aware)                                                             | Same (MVS raises it)                            |
| Go stdlib                    | Raise the `toolchain` directive (and the `go` line only when required), then bump the builder image tag in the Dockerfile | n/a                                             |
| npm                          | `npm install pkg@X`                                                                                                       | `overrides` in package.json, then `npm install` |
| pnpm                         | `pnpm update pkg@X`                                                                                                       | `pnpm.overrides`, then `pnpm install`           |
| yarn                         | `yarn up pkg@X`                                                                                                           | `resolutions`                                   |
| pip (requirements\*.txt)     | Rewrite the pin, then `pip-compile` if a `.in` exists                                                                     | Same through constraints                        |
| poetry                       | `poetry add pkg@^X` / `poetry update pkg`                                                                                 | `poetry update pkg`                             |
| uv                           | `uv lock --upgrade-package pkg==X`                                                                                        | Same                                            |
| cargo, bundler (phase 5)     | `cargo update -p pkg --precise X`, `bundle update --conservative pkg`                                                     | Same                                            |
| OS packages (deb/apk/rpm)    | Refresh the `FROM` base image to the newest digest of its tag (or the next patch tag), then rebuild                       | n/a                                             |
| Maven, Gradle, NuGet, others | Not deterministic. Handed to the agent                                                                                    |                                                 |

4. **Verify** (`--verify`). The default is the project's own gate:

   - `task test:short` when a Taskfile has it, otherwise `go build ./... &&
go test -short ./...` or `npm test`.
   - `vuln.patch.verify` in forge.yaml overrides it.
   - Then a rescan of the changed lockfiles confirms each targeted finding is
     gone.

   The result per finding is `fixed | still_present | skipped(reason)`.

With `--pr`, the patcher commits on `forge/vuln-patch/<date>-<short>`, pushes,
and opens the PR with `gh` as the user. The body lists each advisory, installed
→ fixed, the release and envs affected, the verification output and the
console link. Without `--pr` (the workflow's mode), it leaves the tree changed
and prints the JSON. Exit codes are listed in 1.7.

### 4.2 The `vuln-patch` Reliant workflow: agent takeover in the same run

A built-in workflow, `internal/workflow/builtin/vuln-patch.yaml`, shaped like
`autopilot-issue-worker.yaml`. The node names below are the existing node
types:

```yaml
name: vuln-patch
title: Patch vulnerabilities
inputs:
  project:     {type: string}
  env:         {type: string, default: prod}
  advisories:  {type: array, default: []}      # empty = every fixable finding the gate would block
  mode:        {type: enum, values: [deterministic, agent], default: deterministic}
  max_agent_iterations: {type: integer, default: 3}
triggers: []          # manual (StartRun from the web or CLI); see "Automation" below
nodes:
  - create_worktree   # from the default branch
  - run: reliant forge vuln patch {{env}} {{advisories}} --no-pr --verify --json
    when: mode == "deterministic"
  - branch on exit code:
      0 → publish
      1, 2 → agent      # build/tests broke, or nothing deterministic to do
  - agent (restricted preset; inputs: findings, patch JSON, failing verify output)
    loop while verify fails and iterations < max_agent_iterations:
      fix the breakage or apply the non-deterministic bump; never weaken a test
      run: <the same verify command>; run: reliant forge vuln scan . --json
  - approval (only if the agent changed non-manifest code AND the org opted in)
  - git_publish       # branch vuln-patch/<run>
  - action: github/pr.create@1   # as the Reliant GitHub connection; the agent never sees the credential
```

- **Test gate.** The PR opens only when verify passes and the rescan shows the
  targeted findings gone. Otherwise the run ends failed, with the agent's
  summary and a draft PR **not** opened. A red patch is a report, not a PR.
- **Cost.** Agent takeover uses the org's normal LLM spend, and the run shows
  it. `max_agent_iterations` bounds it.
- **Automation (phase 5, opt-in):** a project may add a `triggers:` entry that
  runs `vuln-patch` when the control plane reports a new blocking finding (a
  webhook trigger with `source: vulnerabilities`, the same shape as the
  existing `source: alerting`) or on a weekly schedule.

## 5. Semver and backwards compatibility

forge and reliant are at major 1. Every change here is an addition:

- **forge:**
  - New fields only: `Artifact.SBOM`, `omitempty`, outside identity.
  - SBOM referrers are new manifests beside an image and never change the
    image's own digest. That is why forge attaches a referrer and does not turn
    on `docker buildx --sbom`, which writes an attestation manifest into the
    image index and changes the index digest (to be confirmed in phase 0).
    `--provenance=false` stays.
  - New verbs: the `vuln` group, `tools` entries.
  - New flags: `--no-sbom`, `--sbom`, `--sbom-source`, `--override-vuln-gate`.
  - New forge.yaml keys: `vuln.*`.
  - A new gate step behind its rollout row.
  - The old ledger reader must tolerate the new field. Phase 1 adds a test that
    an older forge's decoder ignores it, and if any reader is strict, the field
    goes in a sidecar file instead.
  - `env build` gets slower by the SBOM time. It is concurrent and cached, with
    `--no-sbom` and `provenance_sbom = off` as escape hatches.
  - `forge ci vuln-scan` is unchanged.
  - The scaffolded `build-images.yml` keeps cosign and drops only the
    duplicated `sbom-action` / `trivy-action` steps once `forge vuln scan`
    covers them. Existing projects keep their file, because templates are the
    project's own.
- **The gate:** it is graded through the rollout table. Nothing blocks a
  project that does not pin a forge at or above `GatesFrom` (or opt in
  early). A release cut by an older forge (no SBOM) shows "not scanned" and is
  never refused by `vuln_gate_unscanned` until its project's pin gates.
- **control-plane:**
  - A new proto file and service.
  - New optional fields only: the `vuln-findings` and `enforces-vuln-gate`
    capabilities in the existing advertisement.
  - New tables, and a widened CHECK (expand).
  - `buf breaking` passes.
  - Old forge clients keep working and simply get no findings.
- **reliant:**
  - New routes, components and a new built-in workflow file (a new name).
  - It uses the existing `StartRun`. No reliant proto change is needed.
  - Old daemons and SDKs are unaffected.

## 6. Phased plan

Sizes: **S** is under a day, **M** a few days, **L** a week or more. Tasks in
the same "parallel group" can run at the same time. → means "blocked by".

### Phase 0: confirm the choices (1 task)

| #   | Task                                                                                                                                                                                                                                                                                               | Repo  | Size | Depends on          |
| --- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----- | ---- | ------------------- |
| 0.1 | Spike: run syft + grype pinned binaries on three fixtures: a forge Go service image, a Vite static site, and an adopted Python Dockerfile app. Measure the time, SBOM size, DB size, and the KEV/EPSS fields in grype output. Confirm offline `db import`. Write the numbers into this doc         | forge | S    | owner decisions 2–3 |
| 0.2 | Spike: attach a plain CycloneDX referrer with oras-go to an image and a static-site artifact on GAR, Docker Hub, GHCR (tag fallback), private ECR, ACR, Harbor, zot and a local distribution v3.1.2. Read it back by digest and through `Referrers`. Copy with `oras cp -r`. Record results in 1.3 | forge | S    | owner decision 1    |

### Phase 1: the forge default, local (no server needed)

| #   | Task                                                                                                                                                                                                                                                             | Repo  | Size | Depends on | Parallel group |
| --- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----- | ---- | ---------- | -------------- |
| F1  | `internal/toolfetch`: pinned, sha256-verified binary installs. Add syft and grype to `toolversions.go`, `forge tools pins`, `forge doctor`                                                                                                                       | forge | M    | 0.1        | A              |
| F2  | `pkg/release`: `SBOMRef` evidence field (excluded from identity), a machine-ledger SBOM and findings store, an old-decoder compatibility test                                                                                                                    | forge | S    | —          | A              |
| F3  | SBOM generation at the cut for every artifact kind (image, prebuilt image, host binary, lockfile, static site, hosted backend, composition), the digest cache, `--no-sbom`, `provenance_sbom` honoured, warn-first on failure                                    | forge | M    | F1, F2     | B              |
| F3b | Attach each OCI artifact's SBOM as a plain referrer with oras-go (tag fallback, GAR `null` quirk tolerated), record `SBOMRef.Referrer`, `vuln.attach_sbom`, the ledger → digest → referrer/ledger/cache read path                                                | forge | S    | F2, 0.2    | B              |
| F4  | `forge vuln scan/list/show/sbom/db` on the machine ledger: DB cache, mirror and import, staleness, the JSON schema, exit codes                                                                                                                                   | forge | M    | F1, F2     | B              |
| F5  | `forge release record`: `--sbom`, auto-generation from `--image`, `--no-sbom`, `--sbom-source`; deploy/record skill                                                                                                                                              | forge | S    | F3         | C              |
| F6  | `vuln_gate` for self-hosted envs: the advisory verdict at the cut, enforcement before the promotion write, rollout rows `vuln-gate` and `sbom-required`, `--override-vuln-gate`, `forge vuln exempt` (writes KCL or the env document), the `vuln-sbom` gate step | forge | M    | F4         | C              |
| F7  | Docs that tell the truth: `concepts.md` and `benefits.md` claims, the delivery-policy skill status table, a new `vuln` skill, the scaffolded `build-images.yml` uses `forge vuln scan`                                                                           | forge | S    | F3, F4, F6 | D              |

### Phase 2: control-plane store, rescans, gate

| #   | Task                                                                                                                                                                                                                                                                                         | Repo                      | Size | Depends on                   | Parallel group |
| --- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------- | ---- | ---------------------------- | -------------- |
| C1  | Proto: `VulnerabilityService`, `vuln_admin`, the capability strings, `vuln:read` in forge `pkg/accesstoken` (a one-line forge PR merged first, then pinned)                                                                                                                                  | control-plane (+ forge S) | S    | —                            | A (with F1/F2) |
| C2  | Migrations and store: `vuln_sboms`, `deploy_release_artifact_sboms`, `vuln_scans`, `vuln_findings`, `vuln_observations`; object storage for SBOM bytes and the retention keep-set; widen the access_tokens CHECK and grant `vuln:read` to holders of `deploy:read`                           | control-plane             | M    | C1                           | B              |
| C3  | `RecordReleaseSBOM` (digest re-derived, size and format limits, `deploy:write`, org-scoped) plus the `vuln_scan` worker (sandboxed grype subprocess, pinned via forge's pin)                                                                                                                 | control-plane             | M    | C2                           | C              |
| C4  | `vuln_db_sync` (mirror with checksum) and `vuln_rescan` (daily, deployed plus 30 days of promoted releases, observations diff, `NotifyOrgAdmins` for new KEV or fixable critical, per-org fairness); `RescanNow` admin                                                                       | control-plane             | M    | C3                           | D              |
| C5  | Read API: `GetVulnerabilitySummary`, `ListVulnerabilities`, `GetVulnerability`, `GetReleaseVulnerabilities` with `vuln:read`, env-bound tokens, NotFound for other orgs                                                                                                                      | control-plane             | M    | C2                           | C (with C3)    |
| C6  | Promote-time `vuln_gate` in the promotion transaction, graded by the shape's forge version, `vuln_gate_blocked` / `vuln_gate_unscanned`, the override recorded as a gate, `enforces-vuln-gate` advertised                                                                                    | control-plane             | M    | C3, F6 (shared grading rule) | D              |
| C7  | Hosted registry: record OCI referrer links in the inventory (`referrer_of`, separate from the parent-index `subject_digest`); retention keeps a kept artifact's referrers and `sha256-*` fallback tags and never the reverse; the delivery signer also signs SBOM referrers with the org key | control-plane             | M    | C1                           | C              |
| F8  | forge hosted ledger: upload SBOMs after `CutRelease`, `vuln list/show` read from the control plane, `Unimplemented` handled, the capability shown in the plan                                                                                                                                | forge                     | S    | C1, F3, F4                   | C              |
| P1  | Pin bumps: reliant to the forge with F1–F8 (so `reliant forge vuln` exists), then control-plane in lockstep (`task pin:forge`)                                                                                                                                                               | reliant, control-plane    | S    | F1–F8 merged, released       | E              |

### Phase 3: web (read-only)

| #   | Task                                                                                                               | Repo    | Size | Depends on                                            | Parallel group |
| --- | ------------------------------------------------------------------------------------------------------------------ | ------- | ---- | ----------------------------------------------------- | -------------- |
| W0  | Sync the vendored control-plane proto (`task generate:controlplane`) to a cp commit with C1                        | reliant | S    | C1 merged                                             | —              |
| W1  | Hooks plus the Vulnerabilities page (table, filters, advisory drawer/page, "not scanned" and "scan failed" states) | reliant | M    | W0, C5 (build against mock transport before C5 ships) | A              |
| W2  | Nav row (hidden on a definite empty read, badge) plus the Overview `VulnerabilitiesCard` and app-card dot          | reliant | S    | W0, C5                                                | A              |

### Phase 4: patching

| #   | Task                                                                                                                                                           | Repo    | Size | Depends on                                  | Parallel group |
| --- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- | ---- | ------------------------------------------- | -------------- |
| F9a | `forge vuln patch` core: finding → manifest resolution, target choice, verify, `--pr` via `gh`, JSON and exit codes; the Go and npm/pnpm/yarn patchers         | forge   | M    | F4                                          | A              |
| F9b | Python patchers: pip/pip-compile, poetry, uv                                                                                                                   | forge   | S    | F9a                                         | B              |
| F9c | Dockerfile base image refresh for OS packages, plus Go stdlib via `toolchain`                                                                                  | forge   | S    | F9a                                         | B              |
| R1  | Built-in `vuln-patch.yaml` (deterministic → agent takeover → verify gate → `git_publish` → `github/pr.create@1`) plus scenario tests for each exit-code branch | reliant | M    | F9a released and pinned (P1 or a later pin) | B              |
| W3  | Patch / Patch with agent buttons → `StartRun`, run and PR links, disabled-with-reason states; "Exempt…" through the same PR path                               | reliant | S    | R1, W1                                      | C              |

### Phase 4b: on by default, and our own products adopt it

The owner settled this on 2026-11-20: every forge feature that is safe to have
on is on by default for new projects, and is offered to existing ones through a
`forge project upgrade` playbook. "Safe" means the default can never block a
deploy on its own, so `vuln_gate` and `provenance_sbom` stay `audit`. Once a
feature is built, our own products adopt it, and each adoption is a tracked
task.

SBOM generation needs no opt-in. F3 runs it at every release cut of every
project that pins the release that ships it. So "on by default" is mostly about
the parts a project owns: its CI template and its env declarations.

| #   | Task                                                                                                                                                                                                                                                                                                                                                                                 | Repo                | Size | Depends on |
| --- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------- | ---- | ---------- |
| D0  | Default-on for new projects: `forge project new` (every kind, `--kind site` and `--kind app` included) scaffolds nothing that turns SBOMs off, the scaffolded CI caches the grype DB and runs `forge vuln scan`, and an e2e test proves a fresh scaffold's first cut records an SBOM per artifact                                                                                    | forge               | S    | F3, F4, F7 |
| D1  | Upgrade playbook `migrations/<NEXT_VERSION>/sbom-vulnerabilities` for existing projects: replace the scaffolded `build-images.yml`'s `sbom-action`/`trivy-action` steps with `forge vuln scan` and the DB cache (an automation step where the file is still the scaffold's), and explain `vuln_gate`, `vuln exempt` and `vuln.attach_sbom`. Surfaced by `forge project upgrade list` | forge               | S    | D0         |
| D2  | control-plane dogfood: its releases carry SBOMs (its cut runs a forge with F3), `vuln_store.ENABLED` on in prod with the bucket provisioned (phase 19, an owner-run step), the scanner's egress NetworkPolicy, `vuln-findings` advertised, and its Security page shows findings                                                                                                      | control-plane       | M    | P1, C3–C5  |
| D3  | reliant dogfood: reliant's releases carry SBOMs, uploaded to the control plane, and its Security page shows findings                                                                                                                                                                                                                                                                 | reliant             | S    | P1, D2     |
| D4  | forge dogfood: forge's own release (`scripts/release-forge.sh`) records an SBOM per published binary with `forge release record --sbom` or `forge vuln scan`, and attaches it to the GitHub release                                                                                                                                                                                  | forge               | S    | P1         |
| D5  | landing-page dogfood: the static site's lockfile SBOM is recorded at its cut, which needs only the pin bump                                                                                                                                                                                                                                                                          | landing-page        | S    | P1         |
| D6  | `vuln-patch` runs on our repos: a scheduled trigger (weekly) on control-plane, reliant and forge, opening PRs through the normal review pipeline                                                                                                                                                                                                                                     | reliant + each repo | S    | R1, D2, D3 |

### Phase 5: follow-ups (each optional and independent)

| #   | Task                                                                                                        | Repo                    | Size | Depends on |
| --- | ----------------------------------------------------------------------------------------------------------- | ----------------------- | ---- | ---------- |
| X1  | `source: vulnerabilities` webhook trigger, so a project can auto-run `vuln-patch` on a new blocking finding | control-plane + reliant | S    | C4, R1     |
| X2  | Alert receivers (Slack, in-app) for new KEV and critical findings through the alerting service              | control-plane           | S    | C4         |
| X3  | cargo and bundler patchers                                                                                  | forge                   | S    | F9a        |
| X4  | govulncheck reachability enrichment ("symbol called / not called") on Go findings                           | forge + control-plane   | M    | F4, C3     |
| X5  | VEX: a "not affected" statement as a first-class exemption kind, exported into the CycloneDX                | forge + control-plane   | M    | F6, C6     |

**Critical path to "a hosted project shows real findings":** F1 → F3 → F8 →
P1, with C1 → C2 → C3 in parallel, then W0 → W1. F3b and C7 (referrers and
their retention) run beside it and do not block findings, because the
control-plane copy carries the scan. Phase 1 stands alone: a
self-hosted project gets SBOMs, findings and a real gate without control-plane
changes. Phase 2 can start on the same day (C1 alongside F1/F2), and the web
work starts against a mock transport as soon as C1's proto merges.

## 7. Decisions for the owner

1. **Where the SBOM lives** (see 1.3).
   (a) The digest goes in the release record. The bytes are an OCI 1.1
   referrer next to each OCI artifact (images and static sites), with the tag
   fallback where needed. Non-OCI artifacts use ledger storage, findings are
   derived, and a Reliant copy is a cache only. (b) A forge/Reliant bucket is
   the source of truth for every SBOM, with the registry untouched. (c) The
   bytes go only in the release record or ledger, and nothing is written to
   registries.
   **Recommend (a)**, which is the proposed lean with four amendments: a plain
   referrer with signing added separately, non-OCI SBOMs in the ledger rather
   than a hosting bucket, the ledger digest as the lookup key, and the Reliant
   copy as a cache.
   - (b) gives standalone and self-hosted forge users nothing unless they run
     a bucket, and splits the SBOM from the image it describes the moment that
     image is copied to another registry.
   - (c) is simpler, but registry-native tools (admission controllers, registry
     UIs, `oras discover`) cannot find the SBOM.
   - The cost of (a): GHCR, quay and our own hosted registry need the tag
     fallback, and our registry retention must learn referrer links (C7).
2. **Scanner and DB.**
   (a) syft + grype; (b) osv-scanner v2 / OSV.dev; (c) Trivy.
   **Recommend (a).** It has the most mature distro coverage, which adopted
   Dockerfile apps need. It has one checksummed DB file that is easy to mirror
   and import offline, the CycloneDX/SPDX pipeline is consistent, and the KCL
   already names grype. OSV data arrives through grype's DB anyway.
3. **How forge delivers the tools.**
   (a) Pinned release binaries with sha256 compiled into forge (a new, small
   `toolfetch` mechanism); (b) embed syft/grype as Go libraries in forge; (c)
   `go install` like every tool today.
   **Recommend (a).** (c) breaks the zero-setup promise for non-Go projects
   with no Go toolchain. (b) adds a very large dependency tree to every forge
   binary and ties scanner fixes to forge releases. (a) keeps forge lean and
   gives control-plane the identical scanner through its forge pin.
4. **Who is authoritative for findings on a control-plane env.**
   (a) The control plane scans the SBOM bytes and its findings are the record.
   forge's cut-time scan is advisory. (b) forge uploads its own findings, and
   the control plane stores them and rescans daily.
   **Recommend (a).** The gate and the UI must not trust a client that could
   omit findings, and one scanner version and one DB per pass makes verdicts
   reproducible. The bytes reach the control plane by upload, because it has
   no pull credentials into users' private registries (Decision 1).
5. **The default `vuln_gate` mode once it is real.**
   (a) Keep `audit` as the schema default: findings, alerts and the UI for
   everyone, and `enforce` as a one-line opt-in; (b) scaffold `enforce` into new
   projects' prod; (c) make `enforce` the schema default for envs with a control
   plane (after the rollout).
   **Recommend (a).** It follows delivery-policy's own rule: a control that can
   refuse a deploy defaults to `audit`. A new advisory can otherwise block an
   unrelated roll-forward fix during an incident. Revisit after a quarter of
   would-block data.

Not a decision, but worth a veto if you disagree: findings are read under a
new `vuln:read` scope rather than `deploy:read` (2.4). A list of exploitable
vulnerabilities is more sensitive than deploy history, and every current
`deploy:read` holder gets the new scope on day one.

## Decisions (owner, 2026-11-20)

The owner approved this design and took the recommended option on all five
decisions in section 7, and kept the separate `vuln:read` scope. These are
settled; implementation follows them and does not reopen them.

1. **Where the SBOM lives: (a).** The SBOM is an OCI 1.1 referrer next to the
   image or static-site artifact, with the tag fallback where the registry has
   no referrers API. The release record holds the SBOM digest. Non-OCI
   artifacts keep their SBOM in the ledger's own storage. The Reliant copy is a
   scan cache only.
2. **Scanner and DB: (a).** syft generates the SBOM and grype scans it.
3. **Tool delivery: (a).** forge installs pinned syft and grype release
   binaries, verified against sha256 values built into forge (`toolfetch`).
4. **Authority on a control-plane env: (a).** The control plane scans the
   uploaded SBOM and its findings are the record. forge's cut-time scan is
   advisory.
5. **Default `vuln_gate` mode: (a).** `audit` stays the schema default;
   `enforce` is a one-line opt-in.

**`vuln:read`:** findings are read under the new `vuln:read` scope, granted on
day one to every current holder of `deploy:read` (2.4).

The owner's framing: vulnerabilities and SBOMs are a forge feature given by
default. When forge scaffolds someone's code, they get all of it.

### Implementation decisions (owner, 2026-11-20)

These came up during implementation. The owner took the recommended option on
each one, and they are settled.

1. **A source-tree SBOM is release evidence (F5).** `forge release record
--sbom-source <dir>` records the SBOM as `Release.SourceSBOM`, which is
   evidence outside the release's identity. It never adds an artifact, so
   recording the same version with or without the flag is the same release.
2. **Bundles carry no SBOM (C7).** Delivery bundles hold rendered config, not
   packages, and section 1.1 gives them no SBOM. The images and sites a bundle
   deploys carry their own. If a bundle referrer is ever wanted, the control
   plane attaches it with its publisher identity. sigguard is never loosened
   for tenants on `bundle.v1/` paths.
3. **forge's tool pins are a public package (F1b).** `pkg/toolpins` exports the
   syft and grype release pins: versions, archives, sha256. It is held to v1
   semver. control-plane's scanner image asserts its grype pin against it, so
   the two cannot drift.
4. **`provenance_sbom` defaults to `audit`.** The recorded release shape
   cannot tell an explicit `enforce` from the schema default, and no release
   ever evaluated the control. So the KCL default moves to `audit` before any
   enforcement ships (F6). This applies decision 5's rule: a control that can
   refuse a deploy defaults to `audit`.
5. **The control-plane SBOM copies and the grype DB mirror live in a
   dedicated bucket,** `reliant-prod-vulnerabilities` (C3, C4).
6. **Navigation.** The web app shows vulnerabilities and SBOMs under a
   **Security** section of the project nav. It is visible before any scan,
   with a "not scanned yet" state. It is not a separate Vulnerabilities row
   that hides on an empty read (this supersedes W2's "hidden on a definite
   empty read").
7. **On by default, and dogfooded.** New projects get SBOMs and scanning with
   no setup, existing projects get an upgrade playbook, and control-plane,
   reliant, forge and the landing page adopt it after the pin bump. See
   phase 4b.

## Phase 0 results and amendments (2026-11-20)

Phase 0 (tasks 0.1 and 0.2) confirmed syft + grype and the referrer with the
tag fallback. It changes no decision. It fixes the pins and adds five
amendments, which the tasks in section 6 follow.

**Pins.** syft v1.57.2 and grype v0.122.1, each archive's sha256 checked
against the upstream `checksums.txt`. grype publishes no windows/arm64 build,
so that platform falls back to "scanner missing" (not scanned). The grype DB is
schema v6.1.10, named with its sha256 by
`https://grype.anchore.io/databases/v6/latest.json`. forge keeps oras-go
v2.7.0.

**Measurements.** syft takes 1.3 to 4 s on an image or a lockfile, and 13.7 s
when it pulls a 115 MB image from a registry. A CycloneDX SBOM is 300 KB to
1 MB. grype scans an SBOM in about 3 s with a warm DB. The DB is a 205 MB
archive that unpacks to 3.3 GiB. `grype db import` takes about 2 minutes of
CPU, and a cold first scan about 93 s.

**Amendments:**

1. **Referrer attach (F3b): set `SkipReferrersGC = true`.** On a tag-fallback
   registry, oras-go v2.7.0 deletes the old referrers index after it moves the
   `sha256-<hex>` tag. Our hosted registry's tenant tokens cannot delete, so
   the second attach to a subject reported an error after the tag had already
   moved. The orphaned index is untagged, and registry GC reclaims it.
2. **Serialize attaches per subject (F3b).** On tag-fallback registries,
   concurrent attaches to one digest lost index entries in every trial. The
   referrer manifests were stored but not listed. forge takes a per-subject
   lock in the process, lists `Referrers` after each attach, and re-attaches
   the same manifest if it is missing. That is idempotent. A control-plane
   signer that writes its own referrer to the same subject (C7) needs the same
   retry.
3. **The default verdict counts only findings with a fix (F4, F6).** On a
   Debian-based Python image, 222 of 246 matches were `wont-fix` or
   `not-fixed` distro findings. grype's `fix.state` is reliable, so those are
   reported but never block. This is the schema's existing `fixable_only`
   default. CPE-based matches cause most false positives and are shown with
   lower confidence.
4. **One shared grype DB per machine (F4, C4).** It lives in
   `~/.forge/cache/vulndb/`, never per project. CI caches it keyed by the DB's
   build date. The control-plane worker keeps one warm per pod and refreshes it
   in the background. The DB is never downloaded inside a request path. grype
   refuses a DB older than 5 days, which matches the `stale` verdict in 1.2.
5. **Static-site SBOMs come from the lockfile, labelled as such (F3, W1).**
   syft finds no packages in a built `dist/` and cannot read forge's
   static-site OCI artifact. The lockfile with dev dependencies excluded is the
   source (`SBOMRef.Source = lockfile`), and the UI says "from lockfile". It
   overstates the bundle, because build-time tools listed under `dependencies`
   are included. That is accepted for phase 1.

**Other findings:**

- The hosted registry's sigguard refuses any manifest with a `subject` on a
  `bundle.v1/` path from a tenant token. forge does not attach SBOMs there and
  records the reason. The control plane attaches or stores bundle SBOMs itself
  (C3, C7).
- quay supports the referrers API. The earlier research note that said
  otherwise was wrong. GAR, Docker Hub, quay, ECR public, MCR and zot all
  answer the API. GHCR and distribution (v2 and v3) need the tag fallback.
- `oras cp -r` carried the referrer between API and fallback registries in
  every direction, so mirroring and promotion keep the SBOM.
- A GAR "delete untagged" cleanup policy may delete referrers, because a
  referrer is an untagged manifest. This was not tested; F7 documents it.
