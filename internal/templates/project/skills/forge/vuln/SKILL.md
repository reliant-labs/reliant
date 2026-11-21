---
name: vuln
description: SBOMs and vulnerabilities in forge — what every release cut records by default (a CycloneDX SBOM per artifact, stored in the release, the machine ledger and beside the image as an OCI referrer, then scanned with grype), every `forge vuln` verb (scan, list, show, sbom, db status|pull|import, exempt, patch) with exit codes and the `forge.dev/vuln-list/v1` JSON, the shared 3.3 GiB grype database (location, CI cache key, mirror, offline import, the 5-day freshness rule), pinned syft/grype installs, mirrors, `pkg/toolpins` and the tool cache's trust check, SBOM sources per artifact kind (static sites are "from lockfile"), `--no-sbom`, `provenance_sbom = off`, `vuln.attach_sbom`, registry referrer support and the GAR cleanup-policy caveat, `vuln_gate` audit/enforce and its warn-first rollout, `forge vuln exempt`, `--override-vuln-gate`, `forge vuln patch` and `vuln.patch.verify`, recording an SBOM for a deploy your own CI did, the `vuln-sbom` gate step, and control-plane envs. Use when a cut says "not scanned", when a finding needs a dependency bump, before turning `vuln_gate` to enforce, or when a promotion is refused `vuln_gate_blocked`, `vuln_gate_unscanned` or `sbom_missing`.
---

# SBOMs and vulnerabilities

forge answers "what is in this release, and what is it known to be vulnerable
to?" without any setup. Every release it cuts describes each artifact with an
SBOM, keeps that SBOM with the release, and scans it. Whether a finding can stop
a deploy is a separate, opt-in decision: the env's `vuln_gate`.

## What you get by default

At every cut (`forge env build <env> --release v1.2.0`, or the cut inside
`forge env deploy`):

1. **An SBOM per artifact**, CycloneDX 1.6 JSON, written by forge's pinned
   `syft` of **the bytes that ship**, not of the repository (sources below).
2. **Recorded on the release.** `Artifact.SBOM` holds the SBOM's digest, format,
   generator, source and status. It is evidence beside the artifact, not part of
   its identity: the same image digest is the same artifact with or without one.
3. **Stored three ways.** The bytes go to this machine's ledger (always);
   an image or hosted static site also gets the SBOM beside it in its registry as
   an OCI 1.1 referrer (the `sha256-<hex>` tag fallback where the registry has no
   referrers API); an env whose ledger is a control plane gets it uploaded, and
   keeps its own copy there (see "Control-plane envs").
4. **A scan.** If this machine already has the vulnerability database, grype
   scans each SBOM and the cut prints the counts and the env's `vuln_gate`
   verdict. A cut **never downloads the first database** (205 MB): without one it
   prints `not scanned` and the command that fetches it. A cut never fails over
   an SBOM or a scan: an artifact forge could not describe is recorded
   `unavailable` with the reason, and reads as **not scanned, never clean**.

Nothing is refused by default: `vuln_gate` and `provenance_sbom` are `audit`
(`deploy/delivery-policy`), so findings are recorded and a promotion goes ahead.

```bash
forge vuln db pull                  # once per machine: grype + the database
forge env build prod --release v1.2.0
forge vuln list prod                # what the release prod runs is known to be vulnerable to
```

## SBOM sources: what is described

| Artifact | syft reads | Recorded `source` |
|---|---|---|
| Image forge built (go, docker, shell, adopted Dockerfile apps) | the local docker daemon by digest, else the registry | `image` |
| Prebuilt image (hosted backend, a pinned third-party image) | the registry, by digest | `registry` |
| Host binary (`forge.OnHost`) | the file (Go buildinfo lists every linked module) | `binary` |
| Static site (hosted, bucket, Firebase), npm package | the frontend's or package's lockfile, dev dependencies excluded | `lockfile` |
| Go module | its `go.mod` | `lockfile` |
| An SBOM your CI produced (`release record --sbom`) | nothing: recorded verbatim | `ci-provided` |

A static site's SBOM is **"from lockfile"** and **overstates the bundle**: a
built `dist/` carries no package metadata, so the production dependencies in the
lockfile (build tools in `dependencies` included) are the best evidence available.
A git source pin and a frontend's source maps get no SBOM. A variant image (one
digest per platform variant) records `unavailable`: one SBOM per artifact.

Syft's output is cached by what it read (image digest, file hash) and the syft
version in `$FORGE_SBOM_CACHE` (default `<UserCacheDir>/forge/sbom`), so a re-cut of
the same images runs no syft. `forge env build` is slower by the SBOM time on the
first cut; SBOMs are generated concurrently.

## Turning it off

| To | Do |
|---|---|
| skip SBOMs for one cut | `--no-sbom` on `forge env build --release` / `forge env deploy` (warns; each artifact records `skipped: --no-sbom`, which later reads "not scanned") |
| skip them for an env, recording nothing and saying nothing | `provenance_sbom = forge.Control {mode = "off"}` in that env's `delivery_policy` (also turns the cut's scan off) |
| keep the SBOM but not the registry referrer | `vuln.attach_sbom: false` in forge.yaml (the release and the machine ledger still hold it) |

`vuln_gate = enforce` over `provenance_sbom = off` is refused at render: the gate
scans the SBOM.

## The verbs

`forge vuln` reads this machine's ledger. Output is text, or
`forge.dev/vuln-list/v1` JSON with `--json` (`forge project schema vuln-list` is
its JSON Schema). Exit codes (the same for every verb that judges):

| Exit | Means |
|---|---|
| 0 | nothing the env's `vuln_gate` would block |
| 1 | at least one blocking finding (in **any** gate mode: the exit says what was found, the mode decides whether a promotion is refused) |
| 2 | could not determine: an artifact with no SBOM or scan, grype missing, the database absent, findings whose newest scan used a database older than 5 days (`stale`), or a control plane that does not serve findings yet. **Never a pass** |
| 4 | refused: a promotion (see below), or a control plane that will not let you read its findings |

`forge vuln patch` has its own reading of the same codes (`vuln/patch`).

```bash
forge vuln list prod                         # the release prod runs, judged by prod's vuln_gate
forge vuln list prod --release v1.7.0 --severity high --fixable
forge vuln list prod --kev --json | jq '.findings[].id'
forge vuln show prod CVE-2026-1234           # one advisory (a CVE finds its GHSA alias) or one package
forge vuln sbom prod --artifact api > api.cdx.json
forge vuln sbom prod --artifact api --format spdx   # converted with syft
forge vuln scan                              # a directory (default .): a source tree or build output
forge vuln scan --image ghcr.io/acme/api@sha256:…   # an image, by digest only
forge vuln scan sbom.json                    # an SBOM another tool made (CycloneDX or SPDX JSON)
forge vuln scan --release v1.7.0             # every SBOM the release recorded; stores each scan
forge vuln scan --deployed prod --json       # the release prod's ledger says it runs, judged by prod's gate
forge vuln patch prod --verify               # bump what prod's findings name to their fix, then run the gate (vuln/patch)
```

- `list` and `show` judge by the env's own `vuln_gate` and **never download** the
  database or grype; an SBOM never scanned, or scanned with an older database than
  this machine's, is scanned again first and the scan stored. Filters (`--severity`,
  `--fixable`, `--kev`, `--include-exempt`) narrow what is listed, never the verdict.
  `show` matches an advisory id or alias, or a package name or purl, exempt ones
  included.
- `scan` of a directory, image or `--release` is judged by the **default** gate
  (block KEV and fixable critical, warn high); `--deployed <env>` by the env's.
  `scan` refreshes the database when it is more than a day old.
  `--deployed` reads the release the **ledger** says the env runs, not the digests
  in its cluster: `forge env status` is the drift check.
- `--image` refuses a tag: a scan must describe bytes, not a pointer that moves.
- Counted findings: the default gate counts **only findings with a fix** (grype
  `fix.state == fixed`) toward blocking; `not-fixed` and `wont-fix` are listed,
  never gating. A CPE-based match is lower confidence and says so. A finding with
  no severity counts as critical.

## The vulnerability database, syft and grype

One grype database per machine (`$FORGE_VULN_DB_DIR`, default
`<UserCacheDir>/forge/vulndb`; ~205 MB to download, 3.3 GiB unpacked), shared by
every project. `forge vuln scan` refreshes it when it is a day old, `forge vuln db
pull` now; `list`, `show` and the cut never download it. A verdict is determined only
if each artifact's newest scan used a database built within **5 days**; otherwise it
is `stale` (exit 2), and stale outranks block. syft and grype are pinned upstream
release binaries whose sha256 is compiled into forge, in a tool cache forge refuses
when another user could write it. Location, CI caching, mirrors, offline import, the
freshness rule, the cache's trust check, `forge tools pins --archives` and
`pkg/toolpins`: `vuln/database`.

## Where the SBOM lives, and registries

The release record is the index. `forge vuln sbom` reads the machine ledger copy by
digest and verifies the bytes against it; a missing copy is read back from the
referrer the release names, or discovered among the image's CycloneDX referrers.
A referrer is followed only once it proves to be the subject's SBOM: in the
subject's own repository, an OCI manifest of type `application/vnd.cyclonedx+json`
whose `subject` is that image's digest, with a layer of the recorded SBOM digest. A
recorded referrer that fails the check is never read, and bytes from any source are
verified against the release's digest.

The referrer is a plain OCI 1.1 manifest: `artifactType application/vnd.cyclonedx+json`,
one layer holding the SBOM, `subject` = the artifact's manifest. It is attached only
to artifacts **this project pushed** in this cut (a pinned third-party image, or a
prebuilt image another pipeline pushed, gets none, and no warning). A failed attach
warns and leaves the ledger copy; it never fails the cut. `vuln.attach_sbom: false` in
forge.yaml turns attaching off (default on).
Referrers travel with the image through `oras cp -r` (tested between GAR, a tag-fallback registry and zot, in every direction).

forge never names a registry: it probes the referrers API and falls back to the OCI
tag schema, so **every registry that accepts an OCI image manifest with a `subject`
works** for writing; they differ on read:

| Registry | Referrers API | What forge does |
|---|---|---|
| GAR / gcr.io, Docker Hub, quay.io, Amazon ECR, MCR, zot | yes | listed through the API |
| GHCR, `distribution` v2/v3, Reliant's hosted registry | no | forge maintains the `sha256-<hex>` fallback tag index itself |
| `bundle.v1` repositories on the hosted registry | refused (a tenant token may not push a manifest with `subject` there) | no referrer; the bundle carries no SBOM, the ledger copy covers the release |

(Probed in the design's phase 0 for GAR, the hosted registry, distribution and zot;
the others are the API's published support and untested for writes.) Tag-fallback
registries lose concurrent writes to the index; forge serializes attaches per subject
and re-checks after each, and its orphaned old indexes are left for the registry's GC
(a tenant token cannot delete).

**Warning, not tested: a "delete untagged" cleanup policy.** An OCI referrer is an
untagged manifest. On Google Artifact Registry a cleanup policy that deletes
untagged versions may delete SBOM referrers (and the image signatures that ride the
same mechanism) after the policy's age. Exempt referrers from such a policy, or set
`vuln.attach_sbom: false` so nothing depends on them: the release and the machine
ledger keep the SBOM either way.

## The gate: `vuln_gate` and `provenance_sbom`

Declared per env (`deploy/delivery-policy` has the schema):

```kcl
delivery_policy = forge.DeliveryPolicy {
    vuln_gate = forge.VulnPolicy {mode = "enforce"}    # KEV + fixable criticals block
}
```

| Mode | Means |
|---|---|
| `audit` (default) | findings are recorded and printed as "would block"; nothing is refused |
| `enforce` | a self-hosted promotion is refused `vuln_gate_blocked`, `vuln_gate_unscanned` or `sbom_missing` (exit 4) |

- `vuln_gate_blocked`: a KEV-listed advisory, or one at or above `block_severity`
  (default critical) that has a fix.
- `vuln_gate_unscanned`: an artifact with no SBOM or no scan, or findings from a
  database older than 5 days. Could not determine is never a pass.
- `sbom_missing`: a declared `provenance_sbom = forge.Control {mode = "enforce"}`
  (the schema default is `audit`) and an artifact the release ships has no SBOM.

**Warn first.** Both are graded through the lint rollout rows `vuln-gate` and
`sbom-required`: introduced in v1.10.0 (the first release with SBOMs and `forge
vuln`), gating from **v1.11.0** (`ci/lint-rollout`). While the project's
`forge_version` is below v1.11.0, an `enforce` is judged as `audit` and says `would
block (new in v1.10.0; gates from v1.11.0)`; a pin that jumps the window warns at
that pin and gates from the next.
`lint.rules: {vuln-gate: error}` (or `{sbom-required: error}`) gates now; `{vuln-gate:
warn}` extends the warning. A release an older forge cut (no SBOMs) is never refused
for lacking one.

**Accepting an advisory.** `forge vuln exempt` writes a time-boxed
`forge.VulnExemption` into the env's `deploy/kcl/<env>/main.k` (or its env document),
renders the env, and keeps the edit only if the rendered policy carries it:

```bash
forge vuln exempt prod CVE-2026-1234 --reason "unreachable: we never call the parser" --expires 2026-12-31
```

The reason is one line a reviewer can check; `--expires` is the last day it applies
(not in the past); a CVE id covers the GHSA of the same advisory. Running it again
with the same values changes nothing, with new ones replaces the entry. Review and
commit it.

The edit is made through KCL's own parser, not text matching: it finds the literal
`VulnExemption`, `VulnPolicy`, `DeliveryPolicy` or `Bundle` the env declares and
splices one line in, leaving the rest of the file byte for byte. It **fails closed**:
when it cannot be sure which declaration is effective (two policies, a computed
`vuln_gate` or exemptions list, the id appearing somewhere it cannot read, a file
that does not parse), it writes nothing and prints the exact lines to add by hand.

**Past a refusal, once.** `forge env deploy prod v1.2.0 --override-vuln-gate "<why>"`
promotes past a `vuln_gate` or `provenance_sbom` refusal and records who, why and
which findings on the promotion (as the `vuln_gate` / `provenance_sbom` gates). The
durable answer is an exemption.

## Fixing findings: `forge vuln patch`

`forge vuln patch <env|dir>` bumps each vulnerable Go and npm/pnpm/yarn dependency
to the lowest version that fixes it inside its major, with the ecosystem's own tool,
moves a base image's `FROM` for an OS package and raises the Go toolchain for a
standard-library finding;
`--verify` runs your gate (`vuln.patch.verify` in forge.yaml) and rescans; `--pr`
opens the pull request. Exit 0 patched, 1 a bump or the verification failed, 2
nothing deterministic to do, 4 refused. Ecosystems, what is skipped, the verify
command and every flag: `vuln/patch`.

## Deploys your own CI does

`forge release record` (`deploy/record`) records an SBOM per workload too. Each
`--image` is described from its registry by digest, with the registry credentials the
CI already has. `--sbom <workload>=<path>` (repeatable) records an SBOM your pipeline
produced instead, CycloneDX or SPDX JSON, verbatim (`source: ci-provided`);
`--no-sbom` records none, and each workload reads "not scanned". `--sbom-source <dir>`
describes a version-only record's source tree by its lockfiles and records it as the
release's source SBOM (`Release.SourceSBOM`): evidence outside the release's identity,
so a later record of the same version without it is no conflict. Under `provenance_sbom
= off` nothing is generated from `--image`, but what `--sbom` and `--sbom-source`
explicitly supply is still recorded. An SBOM that cannot be produced warns and the
record still succeeds; a flag mistake (an `--sbom` for a workload the record does not
name, a file that is not an SBOM) refuses the record before anything is written.
`--json` gains an `sboms` list.

## In `task ci`: the `vuln-sbom` step

`forge ci vuln-sbom` describes the source tree's lockfiles with syft (node_modules,
`.git` and `.forge` excluded) and scans that SBOM, judged by the default gate with
`ci.vuln_scan.exemptions` applied: it is what the cut will scan, seen before the cut.
It runs beside `vuln-go` (govulncheck, reachability) and `vuln-npm` (npm audit), which
keep their meaning. It is graded through the same `vuln-gate` row: below it, a blocking
finding is printed and exits 0 and a machine with no scanner or database is an allowed
SKIP (exit 78); from it, a blocking finding exits 1 and a scan that cannot run exits 77,
a SKIP that fails the gate unless `CI_SKIP=vuln-sbom` names it.

## Control-plane envs

An env that declares `forge.ControlPlane` keeps its releases there, and **the control
plane is authoritative**: it scans the SBOM it receives and its findings are the
record. forge's cut-time scan for such an env is advisory (the cut says so) and forge
refuses nothing for it at promote; the control plane enforces `vuln_gate` once it
advertises `enforces-vuln-gate`. Reading findings from a control plane needs a token
with the `vuln:read` scope, which `deploy:read` does not imply.

**Upload.** After a cut, forge uploads each SBOM to the control plane when it
advertises the `vuln-sbom-upload` capability (or `vuln-findings`, which implies it);
the machine ledger keeps its own copy whatever the control plane does with it.

**Reads.** `forge vuln list`, `show`, `sbom`, `scan --deployed` and `patch` for such an
env read the control plane's findings when it advertises `vuln-findings`, mapped into
the same `forge.dev/vuln-list/v1` document and the same exit codes. Without a sign-in,
or without `vuln:read`, they exit 4 (refused); a control plane that does not serve
findings yet exits 2, never a guess from this machine's ledger, which holds none of
its releases (`FORGE_LEDGER=machine` reads that ledger instead, for releases it
holds). `forge vuln scan --image <ref@sha256:…>` scans an image of that env yourself.

## Next

- Declaring the controls, and what else is enforced: `deploy/delivery-policy`.
- Recording a deploy your own CI did: `deploy/record`.
- The database, syft and grype, and their pins: `vuln/database`.
- Bumping vulnerable dependencies: `vuln/patch`.
- How the cut and its ledger work: `deploy/release`, `deploy/offline-ledger`.
- The scaffolded workflows, including the scan job: `ci`; the local gate: `ci/gate`.
- A project scaffolded before v1.10.0, whose build-images.yml still runs the
  anchore SBOM action and Trivy: `migrations/v1.10.0/sbom-vulnerabilities`.
