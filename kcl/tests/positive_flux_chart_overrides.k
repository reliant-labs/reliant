"""
Positive test — every hardened default is overridable (the 20% are never
disempowered), and an override of one does not cost the others.
"""

import forge

_ids = lambda c: any -> any { [o.kind + "/" + o.metadata.name for o in c.values.extraObjects] }

# rbac = "chart": the chart's own RBAC and policies are left alone.
_chart = forge.flux_chart(rbac = "chart", network_policies = "chart", priority_class_name = "", priority_class_create = False)
assert_chart_rbac_untouched = "rbac" not in _chart.values and "multitenancy" not in _chart.values
assert_chart_policies_untouched = "policies" not in _chart.values
assert_chart_no_extra_objects = "extraObjects" not in _chart.values
assert_chart_no_priority = "priorityClassName" not in _chart.values.kustomizeController

# rbac = "pinned": the cluster-wide reads stay, impersonation is pinned to one SA.
_pinned = forge.flux_chart(rbac = "pinned")
_pi = _ids(_pinned)
assert_pinned_multitenancy = _pinned.values.multitenancy.enabled == True and _pinned.values.multitenancy.privileged == False
assert_pinned_impersonator = "ClusterRole/cluster-reconciler-impersonator" in _pi
assert_pinned_pinned_by_name = [o for o in _pinned.values.extraObjects if o.metadata.name == "cluster-reconciler-impersonator" and o.kind == "ClusterRole"][0].rules[0].resourceNames == ["reliant-deploy-tenant"]
assert_pinned_keeps_secret_read = len([r for o in _pinned.values.extraObjects if o.metadata.name == "crd-controller" and o.kind == "ClusterRole" for r in o.rules if "secrets" in r.resources]) == 1
assert_pinned_no_tenant_roles = "ClusterRole/flux-tenant-reconciler" not in _pi
# Pinned mode keeps the config watcher (it can list cluster-wide), so no gate to disable it.
assert_pinned_watchers_on = "DisableConfigWatchers" not in str(_pinned.values.kustomizeController.container.additionalArgs)

# A different impersonated SA flows into the default, the pin and the role.
_sa = forge.flux_chart(impersonate_service_account = "flux-deployer")
assert_sa_default = _sa.values.multitenancy.defaultServiceAccount == "flux-deployer"
assert_sa_pinned = [r for o in _sa.values.extraObjects if o.metadata.name == "flux-tenant-reconciler" for r in o.rules if "impersonate" in r.verbs][0].resourceNames == ["flux-deployer"]

# Sizing.
_sz = forge.flux_chart(concurrency = 32, concurrency_ssa = 12, source_concurrency = 4)
assert_sizing = "--concurrent=32" in _sz.values.kustomizeController.container.additionalArgs and "--concurrent-ssa=12" in _sz.values.kustomizeController.container.additionalArgs and _sz.values.sourceController.container.additionalArgs == ["--concurrent=4"]

# A feature gate can be turned off, and another added.
_g = forge.flux_chart(feature_gates = {CancelHealthCheckOnNewRevision = False, DirectSourceFetch = True})
assert_gate_off = "CancelHealthCheckOnNewRevision=false" in str(_g.values.kustomizeController.container.additionalArgs)
assert_gate_added = "DirectSourceFetch=true" in str(_g.values.kustomizeController.container.additionalArgs)

# PSA: another level, or none.
assert_psa_baseline = forge.flux_chart(pod_security = "baseline").namespace_labels["pod-security.kubernetes.io/enforce"] == "baseline"
assert_psa_none = "namespace_labels" not in forge.flux_chart(pod_security = "")

# A PriorityClass the cluster already has: named, not declared.
_named = forge.flux_chart(priority_class_name = "reliant-platform", priority_class_create = False)
assert_named_priority = _named.values.kustomizeController.priorityClassName == "reliant-platform"
assert_named_priority_not_declared = "PriorityClass/reliant-platform" not in _ids(_named)

# values still merge over the posture, and a consumer's extraObjects are appended, not substituted.
_v = forge.flux_chart(values = {
    kustomizeController.resources.requests.cpu = "1"
    sourceController.serviceAccount.annotations = {"iam.gke.io/gcp-service-account" = "flux@example.iam.gserviceaccount.com"}
    extraObjects = [{apiVersion = "v1", kind = "ConfigMap", metadata = {name = "mine", namespace = "flux-system"}}]
})
assert_values_win = _v.values.kustomizeController.resources.requests.cpu == "1" and _v.values.kustomizeController.resources.requests.memory == "512Mi"
assert_values_identity = _v.values.sourceController.serviceAccount.annotations["iam.gke.io/gcp-service-account"] == "flux@example.iam.gserviceaccount.com"
assert_extra_appended = "ConfigMap/mine" in _ids(_v) and "ClusterRole/flux-tenant-reconciler" in _ids(_v) and "NetworkPolicy/allow-egress" in _ids(_v)
assert_values_keeps_hermetic = "--no-remote-bases=true" in _v.values.kustomizeController.container.additionalArgs

# cluster still re-targets (unchanged contract).
assert_no_cluster_by_default = "cluster" not in forge.flux_chart()
