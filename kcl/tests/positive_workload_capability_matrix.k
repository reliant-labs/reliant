"""
Positive test — the workload KIND × CAPABILITY matrix, asserted in BOTH
directions.

WHY BOTH DIRECTIONS. The defect this file guards is not "a capability is
broken"; it is "a capability is ABSENT and nobody can tell whether that
was on purpose". `render_worker` had no RBAC surface for exactly that
reason — it was written as `render_service` minus the Service object, and
minus'd the Role as well. Nothing failed at render time. The manifests
were valid, the Deployment rolled, and the first sweep of the worker's
reconcile loop failed in production with

    workspaces.reliant.dev is forbidden: User "system:serviceaccount:
    control-plane-prod:control-plane-workers" cannot list resource
    "workspaces" in API group "reliant.dev"

A test that only asserted "supported capabilities work" would have been
fully green the entire time. So each capability is pinned twice: the
kinds that HAVE it must render it, and the kinds that deliberately do NOT
must be shown to reject it (negative_workload_*.k) or to omit it. That is
what makes a future gap a failing test rather than a silent absence.

The matrix itself lives in kcl/workloads/capabilities.k, where each
kind's omissions are justified individually. This file pins that the
matrix and the RENDERERS agree — a table nothing reads would rot exactly
as fast as the hand-written expansions it replaced.
"""

import forge.workloads as fw

# ── The kind × capability table (capabilities.k) ────────────────────
# The EXPANSION half of this fixture (a worker's Role, an operator's
# ClusterRole, a tool rendering nothing, cron/job PSA context) moved to Go
# with the renderer: pkg/deploy TestPerKindObjects / TestIdentity /
# TestEveryPodIsPSARestricted. What stays in KCL is the authoring-time
# matrix the Workload checks read, and that every kind the SCHEMA admits
# is admitted by the matrix (and vice versa).

# Every capability a kind supports is ACCEPTED on a Workload of that kind
# (the positive side of the negative_workload_* refusals).
_rules = [{apiGroups = ["reliant.dev"], resources = ["workspaces"], verbs = ["list", "watch"]}]
_accepted = [
    fw.Workload {name = "svc", kind = "service", namespacedRBAC = _rules, ports = [fw.Port {name = "http", port = 8080}], replicas = 2}
    fw.Workload {name = "wrk", kind = "worker", namespacedRBAC = _rules, replicas = 2}
    fw.Workload {name = "crn", kind = "cron", schedule = "@hourly", args = ["x"], namespacedRBAC = _rules}
    fw.Workload {name = "job", kind = "job", args = ["seed"], namespacedRBAC = _rules}
    fw.Workload {name = "ctl", kind = "operator", crds = ["Workspace"], clusterRBAC = _rules, ports = [fw.Port {name = "metrics", port = 8080}]}
    fw.Workload {name = "cli", kind = "tool", args = ["admin"]}
]
assert_every_kind_constructs = len(_accepted) == len(fw.WORKLOAD_KINDS)
assert_kinds_are_the_generated_enum = sorted(fw.WORKLOAD_KINDS) == sorted(["service", "worker", "job", "cron", "operator", "tool"])

assert_worker_is_a_deployment_kind = "worker" in fw.DEPLOYMENT_KINDS
assert_tool_is_not_a_deployment_kind = "tool" not in fw.DEPLOYMENT_KINDS
assert_every_kind_has_a_capability_row = len(fw.WORKLOAD_KINDS) == len(fw.KIND_CAPABILITIES)
# A broadcast job gates what a release rolls out. `cron` is excluded because
# a CronJob pod fires on a SCHEDULE rather than as part of a rollout —
# gating it would re-run the one-shot on every tick, forever.
assert_broadcast_gates_deployment_kinds = "worker" in fw.BROADCAST_GATED_KINDS and "service" in fw.BROADCAST_GATED_KINDS
assert_broadcast_does_not_gate_cron = "cron" not in fw.BROADCAST_GATED_KINDS
assert_broadcast_does_not_gate_tool = "tool" not in fw.BROADCAST_GATED_KINDS

# The `supports` predicate is what both the schema checks and the renderers
# consult, so it is pinned directly rather than only through its effects.
assert_supports_worker_namespaced_rbac = fw.supports("worker", fw.CAP_NAMESPACED_RBAC)
assert_supports_operator_cluster_rbac = fw.supports("operator", fw.CAP_CLUSTER_RBAC)
# An operator's config read and lease are namespace-local, so it has the
# namespaced tier too; the tiers add, they do not replace each other.
assert_operator_supports_namespaced_rbac = fw.supports("operator", fw.CAP_NAMESPACED_RBAC)
assert_tool_does_not_support_namespaced_rbac = not fw.supports("tool", fw.CAP_NAMESPACED_RBAC)
assert_tool_supports_nothing = len(fw.KIND_CAPABILITIES["tool"]) == 0
# An unknown kind supports nothing rather than erroring — Workload.check
# already rejects a bad kind with a message naming the valid ones, and a
# second error from the matrix would fire first and report the capability
# instead of the typo that caused it.
assert_unknown_kind_supports_nothing = not fw.supports("nonesuch", fw.CAP_NAMESPACED_RBAC)
