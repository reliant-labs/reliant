---
name: migrations/v1.10.0/sbom-vulnerabilities
description: SBOMs and vulnerability scanning in an existing project (forge v1.10.0). Every release cut now records a CycloneDX SBOM per artifact, keeps it in the release and this machine's ledger (and beside the image as an OCI referrer), and scans it with forge's pinned grype; nothing to opt in to. The scaffolded `.github/workflows/build-images.yml` of an older pin still runs anchore/sbom-action and aquasecurity/trivy-action. It is scaffold-once, so the pin bump does not rewrite it. `forge project upgrade` re-renders it with `forge vuln scan` and a cached grype database while it is unedited (`forge fix build-images-vuln-scan`); an edited one gets the steps to replace by hand. Also covers `vuln_gate` (audit by default; an `enforce` warns on v1.10.0 and gates from v1.11.0), `forge vuln exempt` and `vuln.attach_sbom`. Use when build-images.yml still names anchore/sbom-action or aquasecurity/trivy-action.
version: v1.10.0
automation: fix:build-images-vuln-scan
detection: grep -qsE 'anchore/sbom-action|aquasecurity/trivy-action' .github/workflows/build-images.yml
---

# SBOMs and vulnerabilities by default (v1.10.0)

From v1.10.0, forge answers "what is in this release, and what is it known to
be vulnerable to?" for every project, with no setup. The release cut does the
work, so most of it reaches an existing project with the pin bump alone. The
one file that does not change by itself is the image workflow forge scaffolded.

## What changes by itself

At every cut (`forge env build <env> --release <v>`, or the cut inside
`forge env deploy`):

- **An SBOM per artifact.** forge's pinned syft describes the bytes that ship,
  as CycloneDX 1.6 JSON: an image from the docker daemon or its registry, a host
  binary from its Go buildinfo, a static site or npm package from its lockfile.
- **Recorded on the release.** Each artifact's SBOM digest, source and status
  sit beside the artifact (evidence, not identity: the same image digest is the
  same artifact with or without one). The bytes go to this machine's ledger, and
  an image or hosted static site also gets the SBOM beside it in its registry as
  an OCI referrer. An env on a control plane uploads it there too.
- **Scanned, when the database is here.** On a machine with forge's grype
  database (`forge vuln db pull`, about 200 MB, once per machine), the cut prints
  the findings and the env's `vuln_gate` verdict. A cut never downloads the
  first database and never fails over an SBOM or a scan: an artifact forge could
  not describe is recorded `unavailable` and reads as "not scanned", never clean.

`forge vuln list <env>`, `show`, `scan`, `sbom`, `exempt` and `patch` read
those records (`forge skill load vuln`).

## What does not change by itself: build-images.yml

A project scaffolded before v1.10.0 has a `.github/workflows/build-images.yml`
that generates an SPDX SBOM with `anchore/sbom-action` (uploaded as a run
artifact and attached with `cosign attest`) and scans the image with
`aquasecurity/trivy-action`. forge v1.10.0 writes it differently for a pin of
v1.10.0 or later:

- no SBOM steps: the release cut records one per artifact itself;
- the Trivy step is replaced by four: restore the grype database cache (keyed by
  UTC day), `forge vuln db pull` when the cache missed, `forge vuln scan --image`
  on the pushed digest, and save the cache. The scan judges the image by the
  default `vuln_gate` rule: a KEV-listed advisory or a critical with a fix fails
  the job, and a scan that could not run fails it too.

The file is scaffold-once: forge never rewrites it on `forge generate`, so moving
`forge_version` does not change it.

### An unedited build-images.yml

`forge project upgrade --to v1.10.0` (or later) moves the pin and then runs this
playbook's automation, `forge fix build-images-vuln-scan`. It re-renders the
file exactly as `forge generate` would write it for the project today, but only
when the file is byte for byte what an older forge scaffolded for it. To run it
on its own after a pin bump you made by hand:

```bash
forge fix build-images-vuln-scan --dry-run   # what it would rewrite
forge fix build-images-vuln-scan
git diff .github/workflows/build-images.yml
```

It needs `forge_version` at v1.10.0 or later, because CI installs the forge the
project pins and an older one has no `forge vuln`.

### An edited build-images.yml

The codemod leaves a file you changed alone and says so. Either take forge's
file over yours (your edits are lost; the diff shows them first):

```bash
forge project upgrade --check                                  # the diff, among scaffold-once drift
forge project upgrade --force .github/workflows/build-images.yml
```

or edit yours:

1. Delete the steps that use `anchore/sbom-action` and act on its output:
   `Generate SBOM (SPDX JSON)`, `Upload SBOM artifact`, and
   `Attach SBOM to the image (cosign attest)`.
2. Replace the `Run Trivy vulnerability scanner` step with the steps forge
   writes. Copy them from a render rather than by hand: in a scratch directory,
   `forge project new demo --service notes` writes a v1.10.0
   `.github/workflows/build-images.yml` whose `vuln-scan` job has them, from
   `Name today's vulnerability database cache` to
   `Save the vulnerability database`.
3. Run `forge project upgrade apply v1.10.0/sbom-vulnerabilities` once the file
   no longer names either action (the detection then stops matching anyway).

A repository that runs its own scanner can keep it: nothing in forge depends on
these steps. The SBOMs and the gate come from the release cut, not from CI.

## `vuln_gate`: findings are recorded, nothing is refused by default

Each env's `delivery_policy` declares `vuln_gate`, and its schema default is
`audit`: findings are recorded and printed as "would block", and a promotion
goes ahead. Opt an env in to refusing promotions with one line:

```kcl
delivery_policy = forge.DeliveryPolicy {
    vuln_gate = forge.VulnPolicy {mode = "enforce"}    # KEV + fixable criticals block
}
```

`enforce` is warn-first. It is graded through the `vuln-gate` lint rollout row
(and a declared `provenance_sbom = forge.Control {mode = "enforce"}` through
`sbom-required`): new in v1.10.0, gating from **v1.11.0**. While `forge_version`
is below v1.11.0 an `enforce` is judged as `audit` and prints
`would block (new in v1.10.0; gates from v1.11.0)`. `lint.rules: {vuln-gate:
error}` in forge.yaml gates now; `{vuln-gate: warn}` extends the warning. A
release an older forge cut, with no SBOMs, is never refused for lacking one.

Before turning `enforce` on for an env, look at what it would block:

```bash
forge vuln db pull
forge vuln list prod            # exit 1: something would block; 2: not scanned
forge vuln patch prod --verify  # bump what has a fix, run your gate, rescan
```

## `forge vuln exempt`: accepting an advisory, with an end date

When a finding does not apply (the vulnerable code is never reached, a fix is
not out yet), record that in the env instead of turning the gate off:

```bash
forge vuln exempt prod CVE-2026-1234 --reason "unreachable: we never call the parser" --expires 2026-12-31
```

It writes a time-boxed `forge.VulnExemption` into `deploy/kcl/prod/main.k`,
renders the env, and keeps the edit only if the rendered policy carries it. If
it cannot tell which declaration is effective, it writes nothing and prints the
lines to add by hand. Review and commit it. `forge env deploy prod <v>
--override-vuln-gate "<why>"` gets one promotion past a refusal and records who
and why; the exemption is the durable answer.

## `vuln.attach_sbom`: the registry copy

An image's SBOM is attached beside it in its registry as an OCI 1.1 referrer
(the `sha256-<hex>` tag fallback where the registry has no referrers API), so
whoever pulls the image can find it. To keep SBOMs out of the registry, set this
in forge.yaml:

```yaml
vuln:
  attach_sbom: false
```

The release and this machine's ledger still hold every SBOM. Set it when a
registry cleanup policy deletes untagged manifests (Google Artifact Registry's
"delete untagged" may remove referrers), or when the registry refuses them. To
record no SBOMs at all for an env, declare
`provenance_sbom = forge.Control {mode = "off"}` in its `delivery_policy`
(`--no-sbom` skips them for one cut).

More: `forge skill load vuln`, `vuln/database`, `vuln/patch`,
`deploy/delivery-policy`.
