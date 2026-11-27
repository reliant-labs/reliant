# Proposal: an admission check that only runs pods whose images are signed

**Status:** proposal, not implemented. Needs an owner decision (see "To decide").
**Date:** 2026-11-02
**Origin:** forge#1661, moved here on 2026-11-04 and closed. Nothing below has changed since then.

Follow-up to bundle signing (forge#1653, control-plane#1814).

Today the hub verifies the signature on delivery bundles (Flux `spec.verify`), but nothing checks the images the pods in those bundles actually run. A pod spec can name any image the cluster can pull.

Proposal: an opt-in admission rule, rendered by forge for clusters that enable it, that refuses a pod unless every container image is pinned by digest and carries a valid signature from the org's key (or a key the user configures for self-hosted).

To decide:

- Mechanism: a ValidatingAdmissionPolicy can't verify signatures by itself, so we either run a webhook (e.g. sigstore policy-controller or Kyverno `verifyImages`), or have our own small verifier webhook backed by the same per-org keys.
- Modes: off by default; `audit` (warn and record) and `enforce` (refuse), matching `delivery_policy.signing`.
- Who signs images: the hosted builder would sign at build time with the org key; images built elsewhere need a documented way to be signed or allowlisted.
- Exemptions: forge's own platform pods and system namespaces.

Must stay opt-in and add no friction for new projects.
