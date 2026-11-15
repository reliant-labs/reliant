"""
Positive test — `forge.flux_chart()` is a version-pinned forge.HelmChart for
ControlPlane's Flux Operator, with a FluxInstance riding it, rendering through
the SAME `helm_charts` bucket every other platform dep flows through.

WHAT THIS PINS, AND WHY EACH ONE IS HERE RATHER THAN IN A DOCSTRING:

  * THE CHART REFERENCE AND VERSION. `FLUX_CHART_OCI` is the shared-Flux
    detector — `forge cluster up` compares a consumer's declared chart
    against it to decide whether a cluster already has a Flux — and
    pkg/flux holds the same strings Go-side. If they diverged, forge would
    install a SECOND Flux into a cluster that already has one.

  * THE NAME. `flux-operator`, not the community chart's `flux`: forge
    prunes what a chart stops rendering, and the old chart's controller
    Deployments are what the operator adopts in place.

  * NOTHING THIRD-PARTY DRIVES A CHANGE. The operator image is pinned by
    digest, the FluxInstance pins a Flux MINOR and names no distribution
    artifact, so what runs changes only when forge's pin does.

  * TWO COMPONENTS. source-controller and kustomize-controller; every other
    controller is attack surface with nothing on this path.

  * THE OPERATOR IS NOT A WIDER DOOR. No web UI (it would admit every
    namespace on :9080), no aggregation of its kinds into admin/edit, and a
    ResourceSet impersonates a named ServiceAccount by default instead of
    running as the operator. Its ResourceSet/RSIP CRDs and controllers are
    on: nothing here disables them.

  * DELETING IS NOT UNINSTALLING. The FluxInstance carries forge's prune
    opt-out, because the operator uninstalls Flux (CRDs included) when it
    is deleted.

  * `values` MERGES OVER the posture rather than replacing it, and the chart
    still renders as a HelmChart and nothing else.
"""

import forge
import yaml

_bundle = forge.Bundle {
    project = "fixture"
    env = "dev"
    helm_charts = [forge.flux_chart()]
}

output = forge.render(_bundle)
_c = forge.flux_chart()
_fi = [m for m in _c.manifests if m.kind == "FluxInstance"][0]

# ── It is one platform dep in the helm_charts bucket ───────────────
assert_flux_bucket_present = "helm_charts" in output
assert_flux_count = len(output.helm_charts) == 1
assert_flux_name = output.helm_charts[0].name == "flux-operator" and forge.FLUX_CHART_NAME == "flux-operator"

# ── Pinned chart, by reference and version ─────────────────────────
assert_flux_oci = output.helm_charts[0].oci == forge.FLUX_CHART_OCI
assert_flux_oci_literal = output.helm_charts[0].oci == "oci://ghcr.io/controlplaneio-fluxcd/charts/flux-operator"
assert_flux_version = output.helm_charts[0].version == forge.FLUX_CHART_VERSION
assert_flux_version_literal = output.helm_charts[0].version == "0.62.1"
assert_flux_namespace = output.helm_charts[0].namespace == forge.FLUX_NAMESPACE and forge.FLUX_NAMESPACE == "flux-system"
assert_flux_repo_null = output.helm_charts[0].repo == None
# The operator chart ships its CRDs as templates, which `--skip-crds` keeps.
assert_flux_crds_none = output.helm_charts[0].crds == None

# ── The operator's posture ─────────────────────────────────────────
_values = _c.values
assert_image_pinned_by_digest = _values.image.tag == "v0.62.1@sha256:1d09a3f96524d0895fb11e8072d84a3dd029cecdec0f869efa371be6b001469c" and forge.FLUX_OPERATOR_IMAGE.endswith("@" + "sha256:1d09a3f96524d0895fb11e8072d84a3dd029cecdec0f869efa371be6b001469c")
assert_stable_names = _values.fullnameOverride == "flux-operator"
assert_no_web_ui = _values.web.enabled == False
assert_no_aggregation = _values.rbac.createAggregation == False
assert_crds_installed = _values.installCRDs == True
assert_resourceset_impersonates = _values.multitenancy.enabled == True and _values.multitenancy.defaultServiceAccount == "flux-delivery" and forge.FLUX_RESOURCESET_SERVICE_ACCOUNT == "flux-delivery"
assert_no_resourceset_controller_off_switch = "extraArgs" not in _values
# The adoption seam a ResourceSet takes objects over through.
assert_override_managers = forge.flux_chart(override_managers = ["reliant-control-plane-deployapply"]).values.extraArgs == ["--override-manager=reliant-control-plane-deployapply"]
assert_operator_no_service_links = _c.patches["Deployment/flux-operator"].spec.template.spec.enableServiceLinks == False
# "" turns the ResourceSet default off (then a ResourceSet applies as the operator).
assert_resourceset_default_optional = forge.flux_chart(resourceset_service_account = "").values.multitenancy.enabled == False

# ── The FluxInstance ───────────────────────────────────────────────
assert_instance_name = _fi.metadata.name == "flux" and _fi.metadata.namespace == "flux-system" and forge.FLUX_INSTANCE_NAME == "flux"
assert_minor_pinned = _fi.spec.distribution.version == "2.9.x" and forge.FLUX_VERSION == "2.9.x"
assert_no_distribution_artifact = "artifact" not in _fi.spec.distribution and _fi.spec.distribution.registry == "ghcr.io/fluxcd"
assert_two_components = _fi.spec.components == ["source-controller", "kustomize-controller"]
assert_multitenant = _fi.spec.cluster.multitenant == True and _fi.spec.cluster.tenantDefaultServiceAccount == "reliant-deploy-tenant"
# forge declares the NetworkPolicies itself (positive_flux_chart_hardened.k).
assert_instance_policies_off = _fi.spec.cluster.networkPolicy == False
assert_no_sharding_by_default = "sharding" not in _fi.spec
assert_instance_not_pruned = _fi.metadata.annotations["kustomize.toolkit.fluxcd.io/prune"] == "disabled"
# Every CRD the operator renders survives an uninstall.
_crd_patch = [yaml.decode(p.patch) for p in _fi.spec.kustomize.patches if p.target.kind == "CustomResourceDefinition"]
assert_crds_kept = len(_crd_patch) == 1 and _crd_patch[0].metadata.annotations["fluxcd.controlplane.io/prune"] == "disabled"
# The instance rides the chart (after its controllers are Available).
assert_instance_rides_last_but_extras = _c.manifests[-1].kind == "FluxInstance"

# ── A mirror is a distribution override; the version is not ────────
_mirror = forge.flux_chart(distribution = {registry = "registry.example.com/fluxcd", variant = "upstream-alpine", artifact = "oci://registry.example.com/controlplaneio-fluxcd/flux-operator-manifests"})
_md = [m for m in _mirror.manifests if m.kind == "FluxInstance"][0].spec.distribution
assert_mirror = _md.registry == "registry.example.com/fluxcd" and _md.variant == "upstream-alpine" and _md.version == "2.9.x" and _md.artifact.startswith("oci://registry.example.com/")

# ── `values` MERGES OVER the posture, never replaces it ────────────
_merged = forge.flux_chart(values = {image.pullSecrets = [{name = "mirror-pull"}]}).values
assert_flux_override_applied = _merged.image.pullSecrets == [{name = "mirror-pull"}]
assert_flux_override_keeps_digest = _merged.image.tag.startswith("v0.62.1@sha256:")
assert_flux_override_keeps_web_off = _merged.web.enabled == False

# ── extra_objects and instance_patches append ──────────────────────
_more = forge.flux_chart(extra_objects = [{apiVersion = "v1", kind = "ConfigMap", metadata = {name = "extra", namespace = "flux-system"}}], instance_patches = [{target = {kind = "Deployment", name = "source-controller"}, patch = "[]"}])
assert_extra_appended = _more.manifests[-1].metadata.name == "extra"
assert_instance_patch_last = [m for m in _more.manifests if m.kind == "FluxInstance"][0].spec.kustomize.patches[-1].patch == "[]"

# ── Flux NEVER renders into the k8s manifest stream ────────────────
_fluxish = [m for m in output.manifests if "fluxcd" in str(m)]
assert_flux_not_in_manifests = len(_fluxish) == 0
