---
name: deploy/delivery-policy
description: What an env's releases must prove before they ship — `forge.DeliveryPolicy`, the nine per-env controls (trusted builder, signing, provenance/SBOM, Flux verification, admission verification, signed promotions, vulnerability gate, bundle kinds, approvals), their off/audit/enforce modes and defaults, what is mandatory and NOT in the policy, and which controls are recorded but not enforced yet.
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

**Nothing below is enforced yet.** forge validates and records every control;
the enforcement points are control-plane work that has not landed. Until it does,
a control set to `enforce` is a **declaration of intent**: it is in the shape
and the ledger, and nothing refuses a release because of it. The last column says
which task makes it real.

| Control | Default | Why that default | `enforce` does | Enforced by | Status |
|---|---|---|---|---|---|
| `signing` | **enforce** | forge or the platform builder signs on your behalf; nothing for you to set up, nothing it can block | images and the bundle are signed, with the org's KMS key (`key = "org_kms"`), a key you hold (`"customer"` + `key_ref`), or keyless Sigstore (`"keyless"` + OIDC identity) | forge F15 (sign on the builder); control-plane C13 (per-org KMS service, key registry) | recorded, not enforced yet |
| `provenance_sbom` | **enforce** | produced by the same builder run as the signature, at no cost to you | a builder-generated SLSA provenance statement and an SBOM are attached to every artifact and recorded in the ledger | forge F15; control-plane C13 | recorded, not enforced yet |
| `flux_verify` | **enforce** | only checks the signature `signing` produced; cannot refuse anything `signing` did not already make | the converger writes `OCIRepository.spec.verify`, so an unsigned bundle never reaches the Kustomizations | control-plane C1/C13 (converger) | recorded, not enforced yet |
| `trusted_builder` | **audit** | enforcing refuses an artifact built on a laptop or an unlisted CI, so it can block a deploy | RecordBundle refuses a bundle whose provenance names a builder or repo not allowed; the registry issues push tokens to builder identities only | forge F14 (`--builder`); control-plane C14 | recorded, not enforced yet |
| `admission_verify` | **audit** | a pod without a valid signature cannot start; turning this on before every running image is signed takes the env down | the env's namespaces are opted in to Sigstore policy-controller; unsigned images are denied (`audit`: warn only) | forge F17 (policy render); control-plane C15 | recorded, not enforced yet |
| `signed_promotions` | **audit** | the converger refuses an unsigned or replayed promotion; a signing outage then blocks every deploy | a promotion is an in-toto statement signed by the org's pipeline identity; the converger and a webhook verify it, including `previous_digest` | forge F18; control-plane C13, C16 | recorded, not enforced yet |
| `vuln_gate` | **audit** | a new advisory can block a release nobody changed | Grype over the SBOM at record and again at promote; see below | control-plane C12 | recorded, not enforced yet |
| `bundle_kinds` | **audit** | a narrowing can reject a bundle that built yesterday | narrows the kinds a bundle may carry for this env (`allow` / `deny`), on top of the mandatory ingest baseline; empty lists narrow nothing | control-plane C19 (ingest) | recorded, not enforced yet |
| `approvals` | **audit** | `enforce` makes every promotion wait for a person | a promotion stays pending until `required` distinct people approve; `--yes` cannot bypass it | control-plane C12 | recorded, not enforced yet |

The rule behind the defaults: **a control that cannot block a deploy and costs
you nothing defaults to `enforce`; a control that can refuse a deploy defaults to
`audit`**, and one line moves it to `enforce`. `audit` is the safe way to turn a
blocking control on: read its would-deny verdicts for a while, then enforce.

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

`key = "org_kms"` (default): a per-org KMS key the control plane holds, usable
only by builder identities. Private, rotated through KMS versions; the control
plane and KMS admins are in your trust base. `"customer"`: a key you hold;
`key_ref` is its PUBLIC reference (`gcpkms://...`), never key material, and we
only verify. `"keyless"`: public Sigstore, with `oidc_issuer` and `oidc_subject`
naming the identity you trust. **Public Rekor records your image digests, repo
names and signer identities**; choose it knowing that.

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
Because `flux_verify` defaults to `enforce`, turning `signing` off alone is
refused until you also set `flux_verify` to `audit` or `off`.

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
is the default a reader resolves absence to, and a test pins it to the KCL
defaults.
