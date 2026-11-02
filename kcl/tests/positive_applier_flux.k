"""
Positive test — Bundle.applier = "flux": a real env with no control plane,
reconciled by a Flux forge installs in its own cluster.

The declaration alone is complete. It adds ONE bootstrap platform chart,
`flux-delivery` (the Flux Operator, the env's delivery namespace, its
ResourceSet and the apply identities), and projects the resolved delivery
(`output.flux_delivery`) that the Go side derives the identity grants from.
Signing, which nothing performs without a control plane yet, resolves to at
most `audit`, and the render says what was asked.
"""

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    cluster_target = _prod
    applier = "flux"
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
})

_fd = output.flux_delivery
_charts = [h for h in output.helm_charts if h.name == forge.FLUX_DELIVERY_CHART_NAME]
_chart = _charts[0]
_objs = _chart.manifests
_kinds = lambda kind: str -> [any] { [o for o in _objs if o.kind == kind] }
_ns = "forge-delivery-acme-prod"

assert_applier_is_projected = output.applier == "flux"
assert_one_bootstrap_chart = len(_charts) == 1
assert_chart_is_the_flux_operator = _chart.oci == forge.FLUX_CHART_OCI
assert_delivery_namespace = _fd.namespace == _ns and _fd.resourceset == "delivery.prod"
assert_defaults = _fd.apply_identities == True and _fd.delivery_scope == "namespace" and _fd.pull_secret == ""

# The env's delivery lives in its own namespace, created by the bootstrap.
assert_namespace_object = [o.metadata.name for o in _kinds("Namespace")] == [_ns]
# The ResourceSet is the shared template, per env, reading its one input from
# the provider a deploy writes.
_rs = _kinds("ResourceSet")[0]
assert_resourceset = _rs.metadata.name == "delivery.prod" and _rs.metadata.namespace == _ns
assert_same_template = _rs.spec.resourcesTemplate == forge.FLUX_DELIVERY_TEMPLATE
assert_reads_provider = _rs.spec.inputsFrom[0].selector == {matchLabels = {"forge.dev/delivery" = "true"}}
assert_rs_identity = _rs.spec.serviceAccountName == forge.FLUX_DELIVERY_SA

# ApplyIdentities: three apply identities, and the default one is granted nothing.
assert_identities = sorted([o.metadata.name for o in _kinds("ServiceAccount") if o.metadata.namespace == _ns]) == sorted([forge.FLUX_DELIVERY_SA, forge.FLUX_DELIVERY_PRE_SA, forge.FLUX_DELIVERY_APPLY_SA, forge.FLUX_DELIVERY_NONE_SA])
_instance = _kinds("FluxInstance")[0]
assert_flux_lockdown = _instance.spec.cluster.multitenant == True
assert_default_is_the_empty_identity = _instance.spec.cluster.tenantDefaultServiceAccount == forge.FLUX_DELIVERY_NONE_SA
_reconciler = [o for o in _kinds("ClusterRole") if o.metadata.name == forge.FLUX_RECONCILER_ROLE][0]
assert_impersonation_pinned = _reconciler.rules[0].resourceNames == [forge.FLUX_DELIVERY_NONE_SA, forge.FLUX_DELIVERY_PRE_SA, forge.FLUX_DELIVERY_APPLY_SA]
# The controllers reach the delivery namespace, and only through its bindings.
assert_namespace_bindings = sorted([o.roleRef.name for o in _kinds("RoleBinding") if o.metadata.namespace == _ns]) == sorted([forge.FLUX_DELIVERY_SA, forge.FLUX_RECONCILER_ROLE, forge.FLUX_SOURCE_ROLE])
# No delivery object is a Secret: the pull Secret is copied by the bootstrap.
assert_no_secret = len(_kinds("Secret")) == 0

# Signing: unset resolves to off with no control plane (nothing signs a
# self-hosted env's bundles yet; declaring enforce is refused at render).
assert_signing_off = _fd.signing == {requested = "off", effective = "off"}
assert_verify_off = _fd.flux_verify == {requested = "off", effective = "off"}
