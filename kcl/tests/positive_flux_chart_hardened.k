"""
Positive test — `forge.flux_chart()` is hardened and sized for a shared
controller set BY DEFAULT, through the FluxInstance's `kustomize.patches` and
the objects riding the chart.

Each group below pins one default, because each is the answer to a specific way
a shared Flux fails:

  * CONTROLLER AUTHORITY. The distribution binds kustomize-controller to
    cluster-admin and every controller to a cluster-wide Secret reader. Both
    are deleted from the operator's build, forge declares Flux CRs
    cluster-wide plus impersonation and Secret reads only where a RoleBinding
    names flux-tenant-*, and an admission policy refuses any other binding to
    a controller ServiceAccount (so a delete patch that stopped matching fails
    loudly instead of granting cluster-admin).
  * THE FLAGS ARE EXACT. Each controller's args are REPLACED by one list: the
    operator's sharding profile would otherwise append a selector that
    orphans every object labelled with any shard value.
  * HEAD-OF-LINE. The pool is sized up and a fix-forward revision cancels a
    stuck health check.
  * NETWORK. allow-egress admits ingress ONLY from flux-system pods (the
    invariant); there is no allow-webhooks; the FluxInstance's own policies
    are off.
  * PSA `restricted` on flux-system: on the Namespace forge creates AND by
    patch on the one the operator renders, which it would otherwise strip.
"""

import forge
import yaml

_flux = forge.flux_chart()
_extra = _flux.manifests
_by = lambda kind: str, name: str -> any { [o for o in _extra if o.kind == kind and o.metadata.name == name][0] }
_fi = _by("FluxInstance", "flux")
_patches = _fi.spec.kustomize.patches
_for = lambda kind: str, name: str -> [any] { [yaml.decode(p.patch) for p in _patches if p.target.kind == kind and p.target?.name == "^${name}$"] }
_args = lambda name: str -> [str] { [op.value for p in _for("Deployment", name) if typeof(p) == "list" for op in p if op.path == "/spec/template/spec/containers/0/args"][0] }
_pod = lambda name: str -> any { [p for p in _for("Deployment", name) if typeof(p) == "dict"][0].spec.template.spec }
_kc = _args("kustomize-controller")
_sc = _args("source-controller")

# ── Controller authority ───────────────────────────────────────────
_deleted = [yaml.decode(p.patch) | {target = p.target} for p in _patches if "delete" in p.patch]
assert_upstream_grants_deleted = sorted([d.kind for d in _deleted]) == ["ClusterRole", "ClusterRoleBinding"] \
    and all d in _deleted { d.target.name == "^(cluster-reconciler|crd-controller)-flux-system$" and d["$patch"] == "delete" }
assert_default_sa = "--default-service-account=reliant-deploy-tenant" in _kc
_crd = _by("ClusterRole", "crd-controller")
# No Secret and no ConfigMap read cluster-wide.
assert_no_clusterwide_secret_read = len([r for r in _crd.rules if "secrets" in r.resources or "configmaps" in r.resources]) == 0
assert_clusterwide_flux_crs = len([r for r in _crd.rules if "kustomize.toolkit.fluxcd.io" in r.apiGroups]) == 1
assert_no_impersonate_clusterwide = len([r for r in _crd.rules if "impersonate" in r.verbs]) == 0
assert_no_chart_impersonator = len([o for o in _extra if o.metadata.name == "cluster-reconciler-impersonator"]) == 0
_rec = _by("ClusterRole", "flux-tenant-reconciler")
_imp = [r for r in _rec.rules if "impersonate" in r.verbs][0]
assert_impersonate_pinned = _imp.resourceNames == ["reliant-deploy-tenant"] and _imp.resources == ["serviceaccounts"]
assert_reconciler_reads_config = len([r for r in _rec.rules if "secrets" in r.resources]) == 1
_src = _by("ClusterRole", "flux-tenant-source")
assert_source_role_reads_only = len(_src.rules) == 1 and _src.rules[0].verbs == ["get", "list", "watch"]
assert_leader_election_namespaced = _by("Role", "flux-leader-election").metadata.namespace == "flux-system"
assert_no_cluster_admin = len([o for o in _extra if o.kind == "ClusterRoleBinding" and o.roleRef.name == "cluster-admin"]) == 0
# The binding guard: fail-closed, Deny, and it allows exactly forge's roles.
_guard = _by("ValidatingAdmissionPolicy", "flux-controller-bindings")
assert_guard_fail_closed = _guard.spec.failurePolicy == "Fail" and _by("ValidatingAdmissionPolicyBinding", "flux-controller-bindings").spec.validationActions == ["Deny"]
assert_guard_targets_bindings = _guard.spec.matchConstraints.resourceRules[0].resources == ["rolebindings", "clusterrolebindings"]
assert_guard_allowlist = "ClusterRole/crd-controller" in _guard.spec.validations[0].expression and "cluster-admin" not in _guard.spec.validations[0].expression
assert_guard_names_controllers = "kustomize-controller" in _guard.spec.variables[0].expression and "source-controller" in _guard.spec.variables[0].expression
# The roles grant nothing until bound; the bindings helper names them.
_b = forge.flux_namespace_bindings("flux-acme")
assert_bindings_count = len(_b) == 2
assert_bindings_namespaced = all o in _b { o.metadata.namespace == "flux-acme" and o.kind == "RoleBinding" }
assert_bindings_roles = sorted([o.roleRef.name for o in _b]) == ["flux-tenant-reconciler", "flux-tenant-source"]
assert_bindings_subjects = [o.subjects[0].name for o in _b if o.roleRef.name == "flux-tenant-reconciler"] == ["kustomize-controller"]
assert_role_names_exported = forge.FLUX_RECONCILER_ROLE == "flux-tenant-reconciler" and forge.FLUX_SOURCE_ROLE == "flux-tenant-source"

# ── The flags, exactly ─────────────────────────────────────────────
assert_kustomize_args = _kc == [
    "--watch-all-namespaces=true", "--log-level=info", "--log-encoding=json", "--enable-leader-election"
    "--no-remote-bases=true", "--no-cross-namespace-refs=true", "--default-service-account=reliant-deploy-tenant"
    "--concurrent=16", "--concurrent-ssa=8", "--requeue-dependency=10s"
    "--feature-gates=CancelHealthCheckOnNewRevision=true,DisableConfigWatchers=true"
]
assert_source_args = _sc == [
    "--watch-all-namespaces=true", "--log-level=info", "--log-encoding=json", "--enable-leader-election"
    "--storage-path=/data", "--storage-adv-addr=source-controller.$(RUNTIME_NAMESPACE).svc.cluster.local.", "--concurrent=8"
]
# No shard, so no selector on either controller.
assert_no_selector = len([a for a in _kc + _sc if "watch-label-selector" in a]) == 0

# ── Pod posture ────────────────────────────────────────────────────
_kp = _pod("kustomize-controller")
_sp = _pod("source-controller")
assert_requests_limits = _kp.containers[0].resources.requests.cpu == "500m" and _kp.containers[0].resources.limits.memory == "2Gi" and _sp.containers[0].resources.limits.memory == "1Gi"
assert_container_by_name = _kp.containers[0].name == "manager"
assert_priority_class = _kp.priorityClassName == "forge-delivery" and _sp.priorityClassName == "forge-delivery"
assert_no_service_links = _kp.enableServiceLinks == False and _sp.enableServiceLinks == False
_pc = _by("PriorityClass", "forge-delivery")
assert_priority_class_declared = _pc.value == 1000000 and _pc.globalDefault == False
# No placement unless asked for: a default must run on every cluster.
assert_no_default_placement = "nodeSelector" not in _kp and "tolerations" not in _kp and "runtimeClassName" not in _kp

# ── Network policies ───────────────────────────────────────────────
assert_instance_policies_off = _fi.spec.cluster.networkPolicy == False
_np = [o for o in _extra if o.kind == "NetworkPolicy"]
assert_netpol_names = [o.metadata.name for o in _np] == ["allow-egress"]
_ae = _by("NetworkPolicy", "allow-egress")
# THE INVARIANT: ingress to every flux-system pod only from flux-system pods.
assert_egress_selects_all_pods = _ae.spec.podSelector == {}
assert_egress_ingress_only_same_namespace = _ae.spec.ingress == [{"from" = [{podSelector = {}}]}]
assert_egress_has_no_namespace_selector = "namespaceSelector" not in str(_ae.spec.ingress)
assert_egress_egress_open = _ae.spec.egress == [{}]
assert_egress_namespace = _ae.metadata.namespace == "flux-system"

# ── Pod Security on the namespace, both owners ─────────────────────
_psa = {
    "pod-security.kubernetes.io/enforce" = "restricted"
    "pod-security.kubernetes.io/enforce-version" = "latest"
    "pod-security.kubernetes.io/warn" = "restricted"
    "pod-security.kubernetes.io/audit" = "restricted"
}
assert_psa = _flux.namespace_labels == _psa
assert_psa_patched_into_operator_namespace = _for("Namespace", "flux-system")[0].metadata.labels == _psa

# And it goes through the schema and renders.
_out = forge.render(forge.Bundle {project = "fixture", env = "dev", helm_charts = [_flux]})
assert_renders = len(_out.helm_charts) == 1
assert_projected_labels = _out.helm_charts[0].namespace_labels["pod-security.kubernetes.io/enforce"] == "restricted"
