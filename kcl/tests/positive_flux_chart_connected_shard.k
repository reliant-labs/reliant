"""
Positive test — `forge.flux_chart(connected_shard = True)` adds the
FluxInstance shard `connected`: a SECOND kustomize-controller for connected
clusters, and "apply as a service account" is a hard invariant on it, not a
convention.

  * the shard runs as its OWN ServiceAccount (the operator's shards reuse the
    main one, so a patch swaps it) with NO --default-service-account, no
    workload identity, and no impersonate or serviceaccounts access anywhere;
  * the main controller keeps --default-service-account and the tenant
    impersonation on the hub, and watches `notin (connected)` — never the
    operator's `!sharding.fluxcd.io/key`, which would orphan every object
    labelled `shard0`;
  * the operator's source-controller shard is deleted: sources are never
    labelled `connected`, and the main source-controller watches every one;
  * a ValidatingAdmissionPolicy (failurePolicy Fail) refuses a shard
    Kustomization with serviceAccountName / without kubeConfig.secretRef, and a
    kubeConfig Kustomization that is not on the shard; a second refuses a shard
    Deployment with any other ServiceAccount or a --default-service-account
    (behaviour proven against a real apiserver by
    TestFluxConnectedShardAdmission).
"""

import forge
import yaml

_dns = {"to" = [{podSelector.matchLabels = {"k8s-app" = "kube-dns"}}], ports = [{port = 53, protocol = "UDP"}]}
_pub = {"to" = [{ipBlock = {cidr = "0.0.0.0/0", except = ["10.0.0.0/8"]}}], ports = [{port = 443, protocol = "TCP"}]}
_c = forge.flux_chart(connected_shard = True, egress = {
    kustomize = [_dns]
    source = [_dns]
    kustomize_connected = [_dns, _pub]
    operator = [_dns]
})
_x = _c.manifests
_by = lambda kind: str, name: str -> any { [o for o in _x if o.kind == kind and o.metadata.name == name][0] }
_fi = _by("FluxInstance", "flux")
_patches = _fi.spec.kustomize.patches
_for = lambda kind: str, name: str -> [any] { [yaml.decode(p.patch) for p in _patches if p.target.kind == kind and p.target?.name == "^${name}$"] }
_ops = lambda name: str -> [any] { [op for p in _for("Deployment", name) if typeof(p) == "list" for op in p] }
_args = lambda name: str -> [str] { [op.value for op in _ops(name) if op.path == "/spec/template/spec/containers/0/args"][0] }
_shard = _args("kustomize-controller-connected")
_main = _args("kustomize-controller")

# ── The FluxInstance shard, selected by the sharding label ─────────
assert_sharding = _fi.spec.sharding == {key = "sharding.fluxcd.io/key", shards = ["connected"]}
assert_main_selector = _main[-1] == "--watch-label-selector=sharding.fluxcd.io/key notin (connected)"
assert_main_never_bang_selector = len([a for a in _main + _args("source-controller") if "!sharding" in a]) == 0
assert_source_has_no_selector = len([a for a in _args("source-controller") if "watch-label-selector" in a]) == 0
assert_shard_selector = "--watch-label-selector=sharding.fluxcd.io/key=connected" in _shard
assert_shard_hermetic = "--no-cross-namespace-refs=true" in _shard and "--no-remote-bases=true" in _shard
assert_shard_leader_election = "--enable-leader-election" in _shard
assert_shard_own_sa = [op.value for op in _ops("kustomize-controller-connected") if op.path == "/spec/template/spec/serviceAccountName"] == ["kustomize-controller-connected"]
_shard_pod = [p for p in _for("Deployment", "kustomize-controller-connected") if typeof(p) == "dict"][0].spec.template.spec
assert_shard_no_service_links = _shard_pod.enableServiceLinks == False
# The operator's source-controller shard does not survive the build.
_gone = [p.target.kind for p in _patches if "delete" in p.patch and p.target.name == "^source-controller-connected$"]
assert_source_shard_deleted = sorted(_gone) == ["Deployment", "Service"]
# The shard Deployment is the operator's now: forge renders none.
assert_no_forge_shard_deployment = len([o for o in _x if o.kind == "Deployment"]) == 0

# ── The ban: no service account on the shard ───────────────────────
assert_shard_has_no_default_sa_flag = len([a for a in _shard if "default-service-account" in a]) == 0
assert_main_keeps_default_sa = "--default-service-account=reliant-deploy-tenant" in _main and _fi.spec.cluster.tenantDefaultServiceAccount == "reliant-deploy-tenant"
_sa = _by("ServiceAccount", "kustomize-controller-connected")
assert_shard_sa_no_annotations = "annotations" not in _sa.metadata
assert_shard_pod_no_wi = "iam.gke.io/gcp-service-account" not in str(_c) and "nodeSelector" not in _shard_pod
# Every ClusterRole/Role bound to the shard, found by following the bindings.
_bound_roles = lambda -> [str] {
    [b.roleRef.name for b in _x if b.kind in ["ClusterRoleBinding", "RoleBinding"] and "kustomize-controller-connected" in [s.name for s in b.subjects]]
}
_rules = [r for o in _x if o.kind in ["ClusterRole", "Role"] and o.metadata.name in _bound_roles() for r in o.rules]
assert_shard_has_roles = len(_rules) > 0 and "flux-connected-controller" in _bound_roles()
assert_no_impersonate = len([r for r in _rules if "impersonate" in r.verbs]) == 0
assert_no_wildcard_verbs = len([r for r in _rules if "*" in r.verbs or "*" in r.resources]) == 0
assert_no_serviceaccounts_access = len([r for r in _rules if "serviceaccounts" in r.resources or "serviceaccounts/token" in r.resources]) == 0
assert_never_bound_to_tenant_roles = len([n for n in _bound_roles() if n in [forge.FLUX_RECONCILER_ROLE, forge.FLUX_SOURCE_ROLE, "crd-controller"]]) == 0
assert_no_clusterwide_secret_read = len([r for o in _x if o.kind == "ClusterRole" and o.metadata.name == "flux-connected-controller" for r in o.rules if "secrets" in r.resources or "configmaps" in r.resources]) == 0
assert_secret_read_is_namespaced_role = _by("ClusterRole", "flux-connected-reconciler").rules == [{apiGroups = [""], resources = ["secrets"], verbs = ["get", "list", "watch"]}]
_imp = [r for r in _by("ClusterRole", "flux-tenant-reconciler").rules if "impersonate" in r.verbs][0]
assert_main_impersonation_unchanged = _imp.resourceNames == ["reliant-deploy-tenant"]
assert_main_binding_subject_unchanged = len([b for b in forge.flux_namespace_bindings("flux-acme") if "connected" in b.metadata.name]) == 0

# ── Per-namespace binding of the shard ─────────────────────────────
_nb = forge.flux_namespace_bindings("flux-acme", connected_shard = True)
assert_ns_bindings = sorted([b.roleRef.name for b in _nb]) == ["flux-connected-reconciler", "flux-tenant-reconciler", "flux-tenant-source"]
_cb = [b for b in _nb if b.roleRef.name == "flux-connected-reconciler"][0]
assert_ns_binding_subject = _cb.subjects == [{kind = "ServiceAccount", name = "kustomize-controller-connected", namespace = "flux-system"}] and _cb.metadata.namespace == "flux-acme"
assert_tenant_binding_not_given_to_shard = len([b for b in _nb if b.roleRef.name == "flux-tenant-reconciler" and b.subjects[0].name != "kustomize-controller"]) == 0

# ── Egress per controller ──────────────────────────────────────────
assert_connected_policy = _by("NetworkPolicy", "kustomize-controller-connected-egress").spec.egress == [_dns, _pub]
assert_connected_policy_selector = _by("NetworkPolicy", "kustomize-controller-connected-egress").spec.podSelector.matchLabels == {app = "kustomize-controller-connected"}
assert_main_has_no_internet = "ipBlock" not in str(_by("NetworkPolicy", "kustomize-controller-egress").spec.egress)
assert_operator_policy_selector = _by("NetworkPolicy", "flux-operator-egress").spec.podSelector.matchLabels == {"app.kubernetes.io/name" = "flux-operator"}

# ── Admission: failurePolicy Fail, Deny, both directions ───────────
_vap = _by("ValidatingAdmissionPolicy", "flux-connected-shard-no-serviceaccount")
assert_vap_fail_closed = _vap.spec.failurePolicy == "Fail"
assert_vap_binding_denies = _by("ValidatingAdmissionPolicyBinding", "flux-connected-shard-no-serviceaccount").spec.validationActions == ["Deny"]
assert_vap_targets_kustomizations = _vap.spec.matchConstraints.resourceRules[0].resources == ["kustomizations"] and _vap.spec.matchConstraints.resourceRules[0].operations == ["CREATE", "UPDATE"]
assert_vap_four_rules = len(_vap.spec.validations) == 4
_idv = _by("ValidatingAdmissionPolicy", "flux-connected-shard-identity")
assert_identity_fail_closed = _idv.spec.failurePolicy == "Fail" and _by("ValidatingAdmissionPolicyBinding", "flux-connected-shard-identity").spec.validationActions == ["Deny"]
assert_identity_targets_shard_deployment = _idv.spec.matchConstraints.resourceRules[0].resources == ["deployments"] and "kustomize-controller-connected" in _idv.spec.matchConditions[0].expression
assert_identity_two_rules = len(_idv.spec.validations) == 2

assert_leader_election_binding_includes_shard = "kustomize-controller-connected" in [s.name for s in _by("RoleBinding", "flux-leader-election").subjects]

# And the lot passes the schema and renders.
_out = forge.render(forge.Bundle {project = "fixture", env = "dev", helm_charts = [_c]})
assert_renders = len(_out.helm_charts) == 1

# ── Without the flag nothing changes ───────────────────────────────
_plain = forge.flux_chart()
assert_plain_has_no_shard = len([o for o in _plain.manifests if "connected" in o.metadata.name]) == 0 and "sharding" not in [m for m in _plain.manifests if m.kind == "FluxInstance"][0].spec
assert_plain_has_no_shard_patch = len([p for m in _plain.manifests if m.kind == "FluxInstance" for p in m.spec.kustomize.patches if "connected" in str(p)]) == 0
