---
name: deploy/delivery-policy
description: What an env's releases must prove before they ship — `forge.DeliveryPolicy`, the per-env controls (trusted builder, signing, provenance/SBOM, Flux verification, admission verification, signed promotions, vulnerability gate, bundle kinds, approvals, the promotion health gate), their off/audit/enforce modes and defaults (signing and flux_verify depend on whether the env has a control plane), what bundle signing proves, what is mandatory and NOT in the policy, and which controls are recorded but not enforced yet.
---

# Delivery policy

`Bundle.delivery_policy` declares, per env, what a release must prove before it
ships: who built it, that it is signed, that it has no blocking vulnerabilities,
that enough people approved it. forge **validates the declaration and records it
in the release shape**. The control plane enforces it.

```kcl
delivery_policy = forge.DeliveryPolicy {
    trusted_builder   = forge.TrustedBuilder {mode = "enforce", builders = ["cloudbuild:acme-prod/delivery"]}
    admission_verify  = forge.Control {mode = "enforce"}
    signed_promotions = forge.Control {mode = "enforce"}
    vuln_gate         = forge.VulnPolicy {mode = "enforce"}      # KEV + fixable criticals block
    approvals         = forge.Approvals {mode = "enforce"}       # one person
}
```

Unset is valid: every control is at its default. Name only what you change.

## Modes

| Mode | Means |
|---|---|
| `off` | the control does nothing |
| `audit` | records the verdict and alerts; **never blocks** |
| `enforce` | blocks |

## The controls, their defaults, and where each is enforced

**`signing` and `flux_verify` are live on a control plane; everything else is
recorded, not enforced yet.** forge validates and records every control. For the
others the enforcement points are control-plane work that has not landed: a
control set to `enforce` is a **declaration of intent**, in the shape and the
ledger, and nothing refuses a release because of it. The last column says which
task makes it real.

Images are **not signed yet**, and neither are a self-hosted env's bundles: `forge env
deploy` pushes them unsigned. Only an env with a control plane gets its bundles signed
(by the platform, below). (The GitHub Actions `build-images.yml` that `forge project
new` scaffolds does sign the images it pushes, keyless with GitHub's OIDC; nothing
in the release path reads those signatures.)

| Control | Default | Why that default | `enforce` does | Enforced by | Status |
|---|---|---|---|---|---|
| `signing` | **audit** with a control plane; **off** without one | the platform signs every recorded bundle for you; enforcing it refuses a rollback to a release recorded before signing, so an env opts in | Promote refuses a bundle without a published, verified signature by the org's key (`key = "org_kms"`: a per-org OpenBao transit key the control plane holds). `audit` signs, verifies server-side, records the verdict and alerts, never blocks | control-plane delivery signer + publisher (bundles); images: forge F15 | **bundles: enforced** (control plane); images, `customer`, `keyless`: not yet |
| `provenance_sbom` | **enforce** | produced by the same builder run as the signature, at no cost to you | a builder-generated SLSA provenance statement and an SBOM are attached to every artifact and recorded in the ledger | forge F15; control-plane C13 | recorded, not enforced yet |
| `flux_verify` | **audit** with a control plane; **off** without one | as `signing` | the converger writes `OCIRepository.spec.verify` (cosign, the org's public keys in the hub Secret `delivery-signing-keys`), so the hub applies only what the control plane signed; Promote refuses an unverified bundle. `audit` records the verdict and alerts, writes no `spec.verify` | control-plane converger | **enforced** (control plane) |
| `trusted_builder` | **audit** | enforcing refuses an artifact built on a laptop or an unlisted CI, so it can block a deploy | RecordBundle refuses a bundle whose provenance names a builder or repo not allowed; the registry issues push tokens to builder identities only | forge F14 (`--builder`); control-plane C14 | recorded, not enforced yet |
| `admission_verify` | **audit** | a pod without a valid signature cannot start; turning this on before every running image is signed takes the env down | the env's namespaces are opted in to Sigstore policy-controller; unsigned images are denied (`audit`: warn only) | forge F17 (policy render); control-plane C15 | recorded, not enforced yet |
| `signed_promotions` | **audit** | the converger refuses an unsigned or replayed promotion; a signing outage then blocks every deploy | a promotion is an in-toto statement signed by the org's pipeline identity; the converger and a webhook verify it, including `previous_digest` | forge F18; control-plane C13, C16 | recorded, not enforced yet |
| `vuln_gate` | **audit** | a new advisory can block a release nobody changed | Grype over the SBOM at record and again at promote; see below | control-plane C12 | recorded, not enforced yet |
| `bundle_kinds` | **audit** | a narrowing can reject a bundle that built yesterday | narrows the kinds a bundle may carry for this env (`allow` / `deny`), on top of the mandatory ingest baseline; empty lists narrow nothing | control-plane C19 (ingest) | recorded, not enforced yet |
| `approvals` | **audit** | `enforce` makes every promotion wait for a person | a promotion stays pending until `required` distinct people approve; `--yes` cannot bypass it | control-plane C12 | recorded, not enforced yet |
| `health_gate` | **enforce**, gating nothing until `after` names an env | the order is declared, never inferred: declaring `after` is the opt-in | a promotion of release R is refused until every env in `after` holds a PASS release-health verdict for R; see below | the control plane, at promote, in the promotion's transaction | **enforced** by a control plane that advertises `shape-health-gate`; recorded only by one that does not, and by a self-hosted env |

The rule behind the defaults: **a control that cannot block a deploy and costs
you nothing defaults to `enforce`; a control that can refuse a deploy defaults to
`audit`**, and one line moves it to `enforce`. `audit` is the safe way to turn a
blocking control on: read its would-deny verdicts for a while, then enforce.
`signing` and `flux_verify` CAN block (a rollback to a release signed before
them), so they are `audit`.

**Where the env delivers decides `signing` and `flux_verify`.** Unset, they are
`audit` for an env with `control_plane = forge.ControlPlane {}`, and **`off`** for
one without: nothing signs a self-hosted env's bundles yet (forge itself does not
sign; `forge env deploy` applies directly). A self-hosted env may declare `audit`
(it only records "unverifiable"); declaring `enforce` for either, or an enforcing
verifier with `signing` unset, is refused at render. The platform also caps every
env at its own ceiling (`DELIVERY_SIGNING_MAX_MODE`, `audit` until it has signed
every rollback target), so `enforce` on a hosted env takes effect when the
platform raises it; until then the env runs as audit.

### `health_gate`

```kcl
# deploy/kcl/prod/main.k
delivery_policy = forge.DeliveryPolicy {
    health_gate = forge.HealthGate {after = ["staging"]}     # mode defaults to "enforce"
}
```

prod takes release R only once staging has **proved R good**: the control plane
holds a release-health verdict of PASS for staging's newest promotion of R. The
verdict is the control plane's own — its release-health evaluator judges every
promotion once the window closes (`release_health.window`, 15m by default, plus
~2m to settle), with the rule `forge obs check` applies — so a client cannot
skip it or supply it. The check runs inside the promotion's transaction.

| staging's verdict for R | `obs check` exit | prod promote | reason (exit) |
|---|---|---|---|
| PASS | 0 | admitted; a `health_gate` gate is recorded on the promotion | — |
| FAIL | 1 | refused | `health_gate_regressed` (1) |
| INCONCLUSIVE, or R never ran on staging | 2 | refused | `health_gate_unproven` (4) |
| not judged yet | 8 | refused | `health_gate_pending` (4): `forge obs check staging --release R --wait`, then retry |

- A release prod itself already ran with a PASS is admitted without staging: a
  rollback needs no detour.
- `audit` admits and records what `enforce` would have refused.
- **The incident override**: `forge env deploy prod R --override-health-gate
  "<why>"`. It overrides this gate only, and the promotion's `health_gate` gate
  records it: who, why, and what staging showed. (On the wire it is the
  acknowledged code `health_gate` plus the note; the flag spells both.)
- `after` may name several envs; each must have proved R. An env not in this
  project's control plane, or a local one, never proves anything.

### `vuln_gate`

```kcl
vuln_gate = forge.VulnPolicy {
    mode = "enforce"
    block_severity = "critical"   # none | low | medium | high | critical     (default critical)
    warn_severity  = "high"       # warn on findings at/above this that do not block (default high)
    block_kev      = True         # always block CISA KEV-listed advisories    (default True)
    fixable_only   = True         # severity blocks only when a fix exists     (default True)
    exemptions = [forge.VulnExemption {id = "CVE-2026-0001", reason = "unreachable: parser never called", expires = "2026-12-31"}]
}
```

The defaults are the prod-tier policy: block KEV-listed and fixable-critical
findings, warn on the rest. An exemption always carries a reason and an expiry
date and stops applying at that date; it is the same vocabulary as forge's own
`ci.vuln.exemptions`. `warn_severity` must not be stricter than `block_severity`,
and a gate that blocks nothing is refused.

### `approvals`

`required` counts distinct people, the requester included.

- `required = 1` (the default): **one-person approval.** The requester's own
  authorization is the approval. This is what a single-owner org wants, and what
  Reliant's own prod uses.
- `required = 2` (up to 5): the **two-person rule.** It needs `approvers`: the
  org members who may approve, at least `required` of them. Declaring it with no
  approvers (or fewer than `required`) is refused at render, because nobody could
  ever satisfy it.

### `signing`

`key = "org_kms"` (default): a per-org key the control plane holds: an OpenBao
transit key (`delivery-org-<org>`, ECDSA P-256), created on the org's first
recorded bundle. Only the platform's delivery signer can use it, and only to
sign; nothing ever exports it. Rotation adds a key version, and every version's
public key stays in the verify set, so older signatures keep verifying. The
control plane and the OpenBao operators are in your trust base. `"customer"`: a
key you hold; `key_ref` is its PUBLIC reference (`gcpkms://...`), never key
material, and we only verify (**not supported yet**: enforcing with it refuses
every promotion). `"keyless"`: public Sigstore, with `oidc_issuer` and
`oidc_subject` naming the identity you trust (**not supported yet**). **Public
Rekor records your image digests, repo names and signer identities**; choose it
knowing that.

**What a bundle signature proves:** the platform signs a bundle's digest after
RecordBundle admitted it into your ledger, so with `flux_verify = enforce` the
hub applies only what the control plane recorded for your org, even if a hub
object, a registry tag or a ledger row is written by something else. It does
**not** prove where the bundle was built (a stolen `deploy:write` token can still
record one); that is `trusted_builder`.

The signature is cosign's: the `sha256-<hex>.sig` tag beside the bundle in the
platform registry, verified offline against the org's public keys. Flux's
key-based check does not bind the signed payload to the digest it is attached to,
so the platform also verifies that binding itself (the publisher, and Promote in
enforce). A bundle pushed to a registry other than the platform's is not signed.

## Combinations forge refuses

A control set to `enforce` over something that is never produced could never
pass, so it is refused at render, naming both:

| Declared | Why it is refused |
|---|---|
| `flux_verify`, `admission_verify` or `signed_promotions` = `enforce` with `signing` = `off` | nothing would carry a signature |
| `trusted_builder` = `enforce` with `provenance_sbom` = `off` | the builder is read from the provenance |
| `vuln_gate` = `enforce` with `provenance_sbom` = `off` | the gate scans the SBOM |
| `approvals.required` > 1 with no approvers, fewer than `required`, a duplicate, or mode `off` | nobody could satisfy it, or it would never apply |
| `signing.key = "customer"` without `key_ref`, `"keyless"` without an OIDC identity, or a field the chosen key model does not use | the declaration does not say whose signature to trust |
| an exemption without a reason or with a date that is not `YYYY-MM-DD` | exemptions are time-boxed and justified |

`audit` over a missing prerequisite is allowed: it only records "unverifiable".
Turning `signing` off alone is accepted, because `flux_verify` defaults to
`audit` (or `off` without a control plane).

| Declared, with no `control_plane` | Why it is refused |
|---|---|
| `signing` or `flux_verify` = `enforce` | nothing signs a self-hosted env's bundles yet |
| `admission_verify` or `signed_promotions` = `enforce` with `signing` unset | `signing` is `off` there, so nothing would carry a signature |

## Not in this policy (mandatory for every env)

These protect other users of the platform and the platform itself, so they are not an env's choice and
there is no mode for them: per-binding apply identity, kubeconfig-ref binding,
narrowed controller impersonation, ledger and pointer integrity, ingest
validation (layout, file types, size, strict YAML, no kustomization files, and the
kinds the env's scope admits), head-of-line fairness, and the hub's PSA and
NetworkPolicy baseline. `bundle_kinds` can only narrow the ingest baseline, never
widen it.

## In the release shape

`forge env shape <env> --json` shows `shape.delivery_policy`, every control spelled
out with lists sorted. The field is **omitted when every value is the default**,
so an env that declares nothing, and one that spells the defaults out, record the
same shape and hash the same; a reader treats absence as "the defaults", never as
"off". Any non-default value records the whole policy. It rides in the bundle's
config blob, so the policy is bound to the bundle digest it was built into.

The Go type is `release.ShapeDeliveryPolicy`; `release.DefaultDeliveryPolicy()`
is the default a reader resolves absence to (the control-plane default, since the
control plane is the reader), and a test pins it to the KCL defaults.
`release.DefaultSelfHostedDeliveryPolicy()` is the no-control-plane default
(`signing` and `flux_verify` off); a self-hosted env that declares
`forge.DeliveryPolicy {}` records it, so its shape says plainly that nothing is
signed.
