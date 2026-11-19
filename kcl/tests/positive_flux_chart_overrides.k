"""
Positive test — every hardened default is overridable (the 20% are never
disempowered), and an override of one does not cost the others.
"""

import forge
import yaml

_ids = lambda c: any -> any { [o.kind + "/" + o.metadata.name for o in c.manifests] }
_fi = lambda c: any -> any { [m for m in c.manifests if m.kind == "FluxInstance"][0] }
_args = lambda c: any, name: str -> [str] {
    [op.value for p in _fi(c).spec.kustomize.patches if p.target.kind == "Deployment" and p.target?.name == "^${name}$" and p.patch.startswith("- ") for op in yaml.decode(p.patch) if op.path == "/spec/template/spec/containers/0/args"][0]
}
_pod = lambda c: any, name: str -> any {
    [yaml.decode(p.patch) for p in _fi(c).spec.kustomize.patches if p.target.kind == "Deployment" and p.target?.name == "^${name}$" and not p.patch.startswith("- ")][0].spec.template.spec
}

# rbac = "upstream", network_policies = "upstream": the distribution's own RBAC
# and policies, no multitenancy lockdown, and forge renders none of its own.
_up = forge.flux_chart(rbac = "upstream", network_policies = "upstream", priority_class_name = "", priority_class_create = False)
assert_upstream_no_lockdown = _fi(_up).spec.cluster.multitenant == False and "tenantDefaultServiceAccount" not in _fi(_up).spec.cluster
assert_upstream_policies_on = _fi(_up).spec.cluster.networkPolicy == True
assert_upstream_grants_kept = len([p for p in _fi(_up).spec.kustomize.patches if "delete" in p.patch]) == 0
assert_upstream_only_the_instance = _ids(_up) == ["FluxInstance/flux"]
assert_upstream_no_default_sa = len([a for a in _args(_up, "kustomize-controller") if "default-service-account" in a]) == 0
assert_upstream_still_hermetic = "--no-remote-bases=true" in _args(_up, "kustomize-controller")
assert_upstream_distribution_priority = "priorityClassName" not in _pod(_up, "kustomize-controller") and "priorityClassName" not in _up.values

# rbac = "pinned": the cluster-wide reads stay, impersonation is pinned to one SA.
_pinned = forge.flux_chart(rbac = "pinned")
_pi = _ids(_pinned)
assert_pinned_multitenant = _fi(_pinned).spec.cluster.multitenant == True
assert_pinned_impersonator = "ClusterRole/cluster-reconciler-impersonator" in _pi
assert_pinned_pinned_by_name = [o for o in _pinned.manifests if o.metadata.name == "cluster-reconciler-impersonator" and o.kind == "ClusterRole"][0].rules[0].resourceNames == ["reliant-deploy-tenant"]
assert_pinned_keeps_secret_read = len([r for o in _pinned.manifests if o.metadata.name == "crd-controller" and o.kind == "ClusterRole" for r in o.rules if "secrets" in r.resources]) == 1
assert_pinned_no_tenant_roles = "ClusterRole/flux-tenant-reconciler" not in _pi
# Pinned mode keeps the config watcher (it can list cluster-wide), so no gate to disable it.
assert_pinned_watchers_on = "DisableConfigWatchers" not in str(_args(_pinned, "kustomize-controller"))
assert_pinned_guarded = "ValidatingAdmissionPolicy/flux-controller-bindings" in _pi

# A different impersonated SA flows into the default, the pin and the role.
_sa = forge.flux_chart(impersonate_service_account = "flux-deployer")
assert_sa_default = "--default-service-account=flux-deployer" in _args(_sa, "kustomize-controller") and _fi(_sa).spec.cluster.tenantDefaultServiceAccount == "flux-deployer"
assert_sa_pinned = [r for o in _sa.manifests if o.metadata.name == "flux-tenant-reconciler" for r in o.rules if "impersonate" in r.verbs][0].resourceNames == ["flux-deployer"]

# Sizing.
_sz = forge.flux_chart(concurrency = 32, concurrency_ssa = 12, source_concurrency = 4)
assert_sizing = "--concurrent=32" in _args(_sz, "kustomize-controller") and "--concurrent-ssa=12" in _args(_sz, "kustomize-controller") and _args(_sz, "source-controller")[-1] == "--concurrent=4"

# A feature gate can be turned off, and another added.
_g = forge.flux_chart(feature_gates = {CancelHealthCheckOnNewRevision = False, DirectSourceFetch = True})
assert_gate_off = "CancelHealthCheckOnNewRevision=false" in str(_args(_g, "kustomize-controller"))
assert_gate_added = "DirectSourceFetch=true" in str(_args(_g, "kustomize-controller"))

# PSA: another level, or none (then neither owner writes the label).
assert_psa_baseline = forge.flux_chart(pod_security = "baseline").namespace_labels["pod-security.kubernetes.io/enforce"] == "baseline"
_nopsa = forge.flux_chart(pod_security = "")
assert_psa_none = "namespace_labels" not in _nopsa and len([p for p in _fi(_nopsa).spec.kustomize.patches if p.target.kind == "Namespace"]) == 0

# A PriorityClass the cluster already has: named, not declared.
_named = forge.flux_chart(priority_class_name = "reliant-platform", priority_class_create = False)
assert_named_priority = _pod(_named, "kustomize-controller").priorityClassName == "reliant-platform" and _named.values.priorityClassName == "reliant-platform"
assert_named_priority_not_declared = "PriorityClass/reliant-platform" not in _ids(_named)

# values still merge over the operator posture.
_v = forge.flux_chart(values = {resources.requests.cpu = "200m", serviceMonitor.create = True})
assert_values_win = _v.values.resources.requests.cpu == "200m" and _v.values.serviceMonitor.create == True
assert_values_keep_posture = _v.values.web.enabled == False and _v.values.rbac.createAggregation == False

# cluster still re-targets (unchanged contract).
assert_no_cluster_by_default = "cluster" not in forge.flux_chart()
