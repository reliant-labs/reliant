"""
Positive test — `forge.flux_chart()` is hardened and sized for a shared
controller set BY DEFAULT (F8 part 1 and the RBAC half of part 2).

Each group below pins one default, because each is the answer to a specific way
a shared Flux fails:

  * CONTROLLER AUTHORITY. The chart's own RBAC lets both controllers
    impersonate ANY ServiceAccount and read EVERY Secret and ConfigMap on the
    cluster. The default replaces it: Flux CRs cluster-wide, and impersonation
    and Secret reads only where a RoleBinding names flux-tenant-*. The
    impersonate grant is pinned by resourceNames.
  * HEAD-OF-LINE. `--concurrent` defaults to 4 and a health check holds a worker
    for its whole timeout, so the pool is sized up and a fix-forward revision
    cancels a stuck check.
  * NETWORK. allow-egress admits ingress ONLY from flux-system pods (the
    invariant); there is no allow-webhooks; allow-scraping admits only the
    namespaces named.
  * PSA `restricted` on flux-system, via namespace_labels.
"""

import forge

_flux = forge.flux_chart()
_values = _flux.values
_kc = _values.kustomizeController
_sc = _values.sourceController
_extra = _values.extraObjects
_by = lambda kind: str, name: str -> any { [o for o in _extra if o.kind == kind and o.metadata.name == name][0] }

# ── Controller authority ───────────────────────────────────────────
assert_rbac_chart_off = _values.rbac.create == False
assert_multitenancy = _values.multitenancy.enabled == True and _values.multitenancy.privileged == False
assert_default_sa = _values.multitenancy.defaultServiceAccount == "reliant-deploy-tenant"

_crd = _by("ClusterRole", "crd-controller")
# No Secret and no ConfigMap read cluster-wide.
assert_no_clusterwide_secret_read = len([r for r in _crd.rules if "secrets" in r.resources or "configmaps" in r.resources]) == 0
assert_clusterwide_flux_crs = len([r for r in _crd.rules if "kustomize.toolkit.fluxcd.io" in r.apiGroups]) == 1
assert_no_impersonate_clusterwide = len([r for r in _crd.rules if "impersonate" in r.verbs]) == 0
# The chart's own over-broad impersonator is not re-declared.
assert_no_chart_impersonator = len([o for o in _extra if o.metadata.name == "cluster-reconciler-impersonator"]) == 0

_rec = _by("ClusterRole", "flux-tenant-reconciler")
_imp = [r for r in _rec.rules if "impersonate" in r.verbs][0]
assert_impersonate_pinned = _imp.resourceNames == ["reliant-deploy-tenant"] and _imp.resources == ["serviceaccounts"]
assert_reconciler_reads_config = len([r for r in _rec.rules if "secrets" in r.resources]) == 1
_src = _by("ClusterRole", "flux-tenant-source")
assert_source_role_reads_only = len(_src.rules) == 1 and _src.rules[0].verbs == ["get", "list", "watch"]

# Leader election is a Role in flux-system, not a cluster-wide write.
assert_leader_election_namespaced = _by("Role", "flux-leader-election").metadata.namespace == "flux-system"
# Nothing binds cluster-admin.
assert_no_cluster_admin = len([o for o in _extra if o.kind == "ClusterRoleBinding" and o.roleRef.name == "cluster-admin"]) == 0

# The roles grant nothing until bound; the bindings helper names them.
_b = forge.flux_namespace_bindings("flux-acme")
assert_bindings_count = len(_b) == 2
assert_bindings_namespaced = all o in _b { o.metadata.namespace == "flux-acme" and o.kind == "RoleBinding" }
assert_bindings_roles = sorted([o.roleRef.name for o in _b]) == ["flux-tenant-reconciler", "flux-tenant-source"]
assert_bindings_subjects = [o.subjects[0].name for o in _b if o.roleRef.name == "flux-tenant-reconciler"] == ["kustomize-controller"]
assert_role_names_exported = forge.FLUX_RECONCILER_ROLE == "flux-tenant-reconciler" and forge.FLUX_SOURCE_ROLE == "flux-tenant-source"

# ── Head-of-line sizing ────────────────────────────────────────────
assert_concurrent = "--concurrent=16" in _kc.container.additionalArgs and "--concurrent-ssa=8" in _kc.container.additionalArgs
assert_requeue = "--requeue-dependency=10s" in _kc.container.additionalArgs
assert_source_concurrent = "--concurrent=8" in _sc.container.additionalArgs
# The verified gate spellings for kustomize-controller v1.9.5.
assert_gates = "--feature-gates=CancelHealthCheckOnNewRevision=true,DisableConfigWatchers=true" in _kc.container.additionalArgs
assert_hermetic_kept = "--no-remote-bases=true" in _kc.container.additionalArgs and "--no-cross-namespace-refs=true" in _kc.container.additionalArgs
assert_requests_limits = _kc.resources.requests.cpu == "500m" and _kc.resources.requests.memory == "512Mi" and _kc.resources.limits.memory == "2Gi" and _sc.resources.limits.memory == "1Gi"
assert_priority_class = _kc.priorityClassName == "forge-delivery" and _sc.priorityClassName == "forge-delivery"
_pc = _by("PriorityClass", "forge-delivery")
assert_priority_class_declared = _pc.value == 1000000 and _pc.globalDefault == False

# ── Network policies ───────────────────────────────────────────────
assert_policies_chart_off = _values.policies.create == False
_np = [o for o in _extra if o.kind == "NetworkPolicy"]
assert_netpol_names = [o.metadata.name for o in _np] == ["allow-egress"]
_ae = _by("NetworkPolicy", "allow-egress")
# THE INVARIANT: ingress to every flux-system pod only from flux-system pods.
assert_egress_selects_all_pods = _ae.spec.podSelector == {}
assert_egress_ingress_only_same_namespace = _ae.spec.ingress == [{"from" = [{podSelector = {}}]}]
assert_egress_has_no_namespace_selector = "namespaceSelector" not in str(_ae.spec.ingress)
assert_egress_egress_open = _ae.spec.egress == [{}]
assert_egress_namespace = _ae.metadata.namespace == "flux-system"

# ── Pod Security on the namespace ──────────────────────────────────
assert_psa = _flux.namespace_labels == {
    "pod-security.kubernetes.io/enforce" = "restricted"
    "pod-security.kubernetes.io/enforce-version" = "latest"
    "pod-security.kubernetes.io/warn" = "restricted"
    "pod-security.kubernetes.io/audit" = "restricted"
}

# ── Unchanged posture ──────────────────────────────────────────────
assert_two_controllers_only = _values.helmController.create == False and _values.notificationController.create == False
# No placement unless asked for: a default must run on every cluster.
assert_no_default_placement = "nodeSelector" not in _kc and "tolerations" not in _kc
assert_no_default_patches = "patches" not in _flux

# And it goes through the schema and renders.
_out = forge.render(forge.Bundle {project = "fixture", env = "dev", helm_charts = [_flux]})
assert_renders = len(_out.helm_charts) == 1
assert_projected_labels = _out.helm_charts[0].namespace_labels["pod-security.kubernetes.io/enforce"] == "restricted"
assert_projected_patches_null = _out.helm_charts[0].patches == None
