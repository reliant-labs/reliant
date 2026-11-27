---
name: vuln
description: SBOMs and vulnerabilities in forge — what every release cut records by default (a CycloneDX SBOM per artifact, stored in the release, the machine ledger and beside the image as an OCI referrer, then scanned with grype), every `forge vuln` verb (scan, gate, list, show, sbom, db status|pull|import, exempt, patch) with exit codes and the `forge.dev/vuln-list/v1` JSON, the shared 3.3 GiB grype database (location, CI cache key, mirror, offline import, the 5-day freshness rule), pinned syft/grype installs, mirrors, `pkg/toolpins` and the tool cache's trust check, SBOM sources per artifact kind (static sites are "from lockfile"), `--no-sbom`, `provenance_sbom = off`, `vuln.attach_sbom`, registry referrer support and the GAR cleanup-policy caveat, `vuln_gate` audit/enforce and its warn-first rollout, `forge vuln exempt`, `--override-vuln-gate`, `forge vuln patch` and `vuln.patch.verify`, recording an SBOM for a deploy your own CI did, the `vuln-sbom` gate step, judging local build output offline with `forge vuln gate --db` (a merge gate's SBOM + scan stage, or a repo forge does not deploy), and control-plane envs. Use when a cut says "not scanned", when a finding needs a dependency bump, before turning `vuln_gate` to enforce, or when a promotion is refused `vuln_gate_blocked`, `vuln_gate_unscanned` or `sbom_missing`.
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
| Prebuilt image (hosted backend, a pinned third-party image), or one carried unchanged from an earlier release (`carried_from`) | the registry, by digest | `registry` |
| Host binary (`forge.OnHost`) | the file (Go buildinfo lists every linked module) | `binary` |
| Static site (hosted, bucket, Firebase), npm package | the frontend's or package's lockfile, dev dependencies excluded | `lockfile` |
| Static site built from a `forge.GitSource` | the lockfile in the **source** checkout at the pinned commit (the frontend's `subdir`, else up to that checkout's root), never this repository's | `lockfile` |
| Go module | its `go.mod` | `lockfile` |
| An SBOM your CI produced (`release record --sbom`) | nothing: recorded verbatim | `ci-provided` |
| A frontend's source maps (`<frontend>.sourcemaps`) | nothing: debugging data, not code that runs | status `not_applicable` |
| A git source pin (`kind: git`) | nothing: it names a commit; the site built from it has its own SBOM | status `not_applicable` |

A static site's SBOM is **"from lockfile"** and **overstates the bundle**: a
built `dist/` carries no package metadata, so the production dependencies in the
lockfile (build tools in `dependencies` included) are the best evidence available.
A variant image (one digest per platform variant) records `unavailable`: one SBOM
per artifact.

**Every artifact is accounted for.** One an SBOM applies to records it (`ok`) or
says why not (`unavailable`, which reads "not scanned"). One it does not apply to
(source maps, a git pin) records `not_applicable` with the reason: it names no
SBOM, and never counts as missing — not in coverage, not in `vuln_gate`, not in
`provenance_sbom`'s `sbom_missing`. Applicability is decided from the artifact
itself (`release.SBOMApplicable` in `pkg/release`), never from the recorded
status. The cut's summary counts the applicable ones and names the rest:

```
[build] SBOMs: 10 of 10 applicable artifacts recorded (6 from cache); 6 not applicable (3 source maps, 3 git source pins)
```

`forge vuln list` says the same (`10 of 10 applicable artifacts scanned; 6 not
applicable (...)`), and its JSON lists them under `coverage.not_applicable`;
`coverage.artifacts` counts only the applicable ones. A forge that predates
`not_applicable` reads it as an unknown status it tolerates, and already leaves
the same artifacts out of coverage.

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
forge vuln sbom --file dist/mycli --artifact mycli_linux_amd64.tar.gz --release v1.2.0 > mycli_linux_amd64.tar.gz.cdx.json
forge vuln scan                              # a directory (default .): a source tree or build output
forge vuln scan --image ghcr.io/acme/api@sha256:…   # an image, by digest only
forge vuln scan sbom.json                    # an SBOM another tool made (CycloneDX or SPDX JSON)
forge vuln scan --release v1.7.0             # every SBOM the release recorded; stores each scan
forge vuln scan --deployed prod --json       # the release prod's ledger says it runs, judged by prod's gate
forge vuln gate dist/ api.tar --db /mnt/vulndb --out sbom/ --env prod --json   # local build output, offline (below)
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
- `sbom --file <path>` reads no env and records nothing: it generates the SBOM of
  one binary or lockfile with the cut's own generator (pinned syft, CycloneDX 1.6,
  `--artifact` as the subject instead of your machine's path, `--release` as its
  version), for binaries your own pipeline publishes. forge's own releases attach
  one per archive this way (`<archive>.cdx.json` on each GitHub release), and scan
  the binaries once with `forge vuln scan <dir>` as an audit that never fails the
  release.
- Counted findings: the default gate counts **only findings with a fix** (grype
  `fix.state == fixed`) toward blocking; `not-fixed` and `wont-fix` are listed,
  never gating. A CPE-based match is lower confidence and says so. A finding with
  no severity counts as critical.

## Local build output, offline: `forge vuln gate`

`forge vuln gate <dir|image.tar|file>... [--sbom <file>] --db <dir> --out <dir> --json`
SBOMs local build output and judges it by a `vuln_gate` with no release and no
ledger, read-only and offline against a mounted database: a merge gate's SBOM +
scan stage, or a repo forge does not deploy. Exit 0 nothing blocks, 1 a blocking
finding, 2 could not determine. Inputs, policy, output and exits: `vuln/gate`.

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

SBOM retrieval, digest verification, OCI referrers, registry support and cleanup
policy caveats: `vuln/registries`.

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

**Scoping an exemption.** Unscoped, an exemption covers the advisory in every package
of every artifact of the env. `--package`, `--artifact` and `--location` (each
repeatable; the `packages`, `artifacts` and `locations` fields of
`forge.VulnExemption`) narrow it: a finding is exempt only where every one given names
it (AND across fields, OR within one).

| Flag | Matches | Example |
|------|---------|---------|
| `--package` | the package name, or its purl exactly or by a prefix ending at `@`, `/`, `?` or `#` | `stdlib`, `pkg:golang/stdlib` |
| `--artifact` | the release artifact's name, exactly | `api` |
| `--location` | an absolute path inside the artifact, exactly, or everything under one ending in `/` | `/usr/local/bin/grype` |

A Go standard-library advisory is the case `--location` exists for: every Go binary's
package is `stdlib` (`pkg:golang/stdlib@go1.x`), so only the path tells an unreachable
copy in a vendored tool from a reachable one in your own binary:

```bash
forge vuln exempt prod GO-2026-6859 --reason "grype never parses untrusted archives" \
  --expires 2026-12-31 --artifact api --location /usr/local/bin/grype
```

`forge vuln list <env> --json` lists each finding's `occurrences` (artifact and path),
which is where the values come from. One finding found in several places (two
artifacts, two binaries of one image) is exempt only when **every** place is covered,
by one exemption or several; anywhere else it still counts. A scope the scan cannot
answer (a finding with no path, against a `--location` exemption) covers nothing.
OS packages list their database and doc files as locations, so scope those by
`--package` and `--artifact` instead.

An exemption's identity is its id **and** its scope: the same id with another scope is
a second entry, added beside the first; the same id and scope with a new reason or
expiry replaces the one entry. The render records scoped exemptions in the shape's
`vuln_gate.scoped_exemptions`, apart from `exemptions`, so a reader that predates
scopes never reads one as an exemption of the advisory everywhere. Hosted declaration
and bundle recording refuse a scoped policy unless the control plane advertises both
`shape-vuln-scoped-exemptions` (decoding) and `evaluates-scoped-vuln-exemptions`
(occurrence-aware evaluation in promotion and vulnerability reads). Upgrade the
control plane before using scoped policies there; decoding alone is not support.
Local evaluation supports scopes. `ci.vuln_scan.exemptions` (forge.yaml) stays unscoped.

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

Below the row it never downloads the database, so on a fresh CI runner it skips and
scans nothing. `forge ci vuln-sbom --pull` downloads it first (into `$FORGE_VULN_DB_DIR`
when set; point that at the runner's temp directory or a cached one, `vuln/database`).

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
  anchore SBOM action and Trivy: `migrations/v1.11.0/sbom-vulnerabilities`.
