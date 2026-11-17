"""
Positive test — a HelmChart declares labels and annotations for the Namespace
forge creates for it, and whether a re-apply prunes the chart's departed objects.

`helm template` never emits the chart's Namespace, so forge creates it; this
is the only place a chart author can declare Pod Security Admission for it
(`pod-security.kubernetes.io/enforce` on `flux-system`).

PINS:
  * namespace_labels / namespace_annotations project through the
    `helm_charts` contract when set;
  * they project null when unset, so a chart that declares none renders a
    byte-identical Namespace to before;
  * `prune` defaults to True and projects False when declared off.
"""

import forge

_flux = forge.flux_chart() | {
    namespace_labels = {
        "pod-security.kubernetes.io/enforce" = "restricted"
        "pod-security.kubernetes.io/enforce-version" = "latest"
    }
    namespace_annotations = {"reliant.dev/owner" = "platform"}
}

_bundle = forge.Bundle {
    project = "fixture"
    env = "dev"
    helm_charts = [
        _flux
        # No metadata declared, prune left at its default.
        forge.HelmChart {
            name = "cert-manager"
            chart = "cert-manager"
            repo = "https://charts.jetstack.io"
            version = "v1.20.1"
            namespace = "cert-manager"
        }
        # Hand-managed live objects: opt out of the prune.
        forge.HelmChart {
            name = "legacy"
            oci = "oci://example.com/legacy"
            version = "1.0.0"
            namespace = "legacy"
            prune = False
        }
    ]
}

output = forge.render(_bundle)

assert_count = len(output.helm_charts) == 3

# ── Declared metadata projects through, key for key ────────────────
assert_labels = output.helm_charts[0].namespace_labels == {
    "pod-security.kubernetes.io/enforce" = "restricted"
    "pod-security.kubernetes.io/enforce-version" = "latest"
}
assert_annotations = output.helm_charts[0].namespace_annotations == {"reliant.dev/owner" = "platform"}

# ── Undeclared metadata projects null: the Namespace is unchanged ──
assert_no_labels = output.helm_charts[1].namespace_labels == None
assert_no_annotations = output.helm_charts[1].namespace_annotations == None

# ── prune defaults on, and projects off when declared off ──────────
assert_prune_default = output.helm_charts[0].prune == True and output.helm_charts[1].prune == True
assert_prune_off = output.helm_charts[2].prune == False
