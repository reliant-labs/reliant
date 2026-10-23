"""
Positive test — `forge.flux_chart()` is a version-pinned forge.HelmChart
that renders through the SAME `helm_charts` bucket every other platform dep
flows through.

WHAT THIS PINS, AND WHY EACH ONE IS HERE RATHER THAN IN A DOCSTRING:

  * THE CHART REFERENCE AND VERSION. `FLUX_CHART_OCI` is the shared-Flux
    detector — `forge cluster up` compares a consumer's declared chart
    against it to decide whether a cluster already has a Flux — and
    internal/flux holds the same two strings Go-side. Three spellings of one
    reference that only agree by inspection is exactly the drift this
    fixture exists to catch: if the chart ref here and the one in Go
    diverge, forge installs a SECOND Flux into a cluster that already has
    one, and the two fight over `flux-system` and the same CRDs.

  * THE TWO CONTROLLERS THAT ARE OFF. helm-controller and
    notification-controller being `create = False` is a security posture,
    not a resource saving, and a chart-default change upstream must not
    silently turn them on. A test that only checked the version would not
    see it.

  * THAT `values` MERGES OVER the posture rather than replacing it. This is
    the seam cp needs for its GAR identity annotation, and the failure mode
    if it replaced instead of merged is quiet: a consumer adding one
    annotation would lose `--no-remote-bases` and every disabled
    controller, and the install would come up looking fine.

  * THAT IT RENDERS AS A HelmChart AND NOTHING ELSE. flux_chart returns a
    plain dict, which `Bundle.helm_charts: [HelmChart]` type-checks at the
    consumer. If that coercion ever stopped happening the chart would still
    "work" as a dict while skipping the schema's `check:` block, so the
    assertion reads the field back THROUGH the rendered bucket.
"""

import forge

_bundle = forge.Bundle {
    project = "fixture"
    env = "dev"
    helm_charts = [forge.flux_chart()]
}

output = forge.render(_bundle)

# ── It is one platform dep in the helm_charts bucket ───────────────
assert_flux_bucket_present = "helm_charts" in output
assert_flux_count = len(output.helm_charts) == 1
assert_flux_name = output.helm_charts[0].name == "flux"

# ── Pinned chart, by reference and version ─────────────────────────
# The exported constants and the rendered chart must agree: a consumer
# detects "this cluster already has a Flux" by comparing against the
# constant, so a chart that pinned something else would be undetectable.
assert_flux_oci = output.helm_charts[0].oci == forge.FLUX_CHART_OCI
assert_flux_oci_literal = output.helm_charts[0].oci == "oci://ghcr.io/fluxcd-community/charts/flux2"
assert_flux_version = output.helm_charts[0].version == forge.FLUX_CHART_VERSION
# Chart 2.19.1 == flux2 v2.9.5 (appVersion). The two numbers are unrelated
# lineages — re-derive with `helm show chart … | grep appVersion` on a bump.
assert_flux_version_literal = output.helm_charts[0].version == "2.19.1"
assert_flux_namespace = output.helm_charts[0].namespace == forge.FLUX_NAMESPACE
assert_flux_namespace_literal = output.helm_charts[0].namespace == "flux-system"

# It is an OCI chart, so the repo channel is unused.
assert_flux_repo_null = output.helm_charts[0].repo == None

# forge supplies no CRD bundle for Flux: the community chart ships its CRDs
# as TEMPLATES, so `helm template --skip-crds` does not drop them and
# forge's CRD-first apply gates them on Established itself.
assert_flux_crds_none = output.helm_charts[0].crds == None

# ── The posture ────────────────────────────────────────────────────
_values = output.helm_charts[0].values
assert_flux_hermetic = "--no-remote-bases=true" in _values.kustomizeController.container.additionalArgs
assert_flux_no_cross_ns = "--no-cross-namespace-refs=true" in _values.kustomizeController.container.additionalArgs

# The hardened default turns multitenancy on and strips the controllers'
# cluster-admin binding; the authority comes back per namespace
# (positive_flux_chart_hardened.k pins every piece). This used to assert the
# opposite, measured against forge's own Flux pointer path, which #673 deleted:
# nothing forge writes into flux-system relies on the controller's own identity.
assert_flux_multitenancy_on = _values.multitenancy.enabled == True and _values.multitenancy.privileged == False

# Only source-controller and kustomize-controller. Each of these four is
# attack surface with nothing on this path to justify it.
assert_flux_no_helm_controller = _values.helmController.create == False
assert_flux_no_notification_controller = _values.notificationController.create == False
assert_flux_no_image_automation = _values.imageAutomationController.create == False
assert_flux_no_image_reflection = _values.imageReflectionController.create == False

# ── `values` MERGES OVER the posture, never replaces it ────────────
# cp's hosted Flux adds a workload-identity annotation to the
# source-controller ServiceAccount. That must not cost it the hermetic flag
# or re-enable a controller.
_with_identity = forge.Bundle {
    project = "fixture"
    env = "dev"
    helm_charts = [forge.flux_chart(values = {
        sourceController.serviceAccount.annotations = {
            "iam.gke.io/gcp-service-account" = "flux@example.iam.gserviceaccount.com"
        }
    })]
}
_merged = forge.render(_with_identity).helm_charts[0].values

assert_flux_override_applied = _merged.sourceController.serviceAccount.annotations["iam.gke.io/gcp-service-account"] == "flux@example.iam.gserviceaccount.com"
assert_flux_override_keeps_hermetic = "--no-remote-bases=true" in _merged.kustomizeController.container.additionalArgs
assert_flux_override_keeps_controllers_off = _merged.helmController.create == False

# ── Flux NEVER renders into the k8s manifest stream ────────────────
# A HelmChart's expansion is a Go-side `helm template` shell-out; KCL emits
# only the declaration. Were Flux's own objects to land in output.manifests
# they would end up INSIDE the bundle — and a bundle whose Kustomization
# prunes what its path no longer carries could then delete its own
# reconciler.
_fluxish = [m for m in output.manifests if "fluxcd" in str(m)]
assert_flux_not_in_manifests = len(_fluxish) == 0
