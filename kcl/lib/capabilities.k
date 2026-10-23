"""
Forge KCL — the workload KIND × CAPABILITY matrix.

THE PROBLEM THIS FILE EXISTS TO SOLVE
=====================================
`Workload.kind` selects an expansion, and each expansion was written by
hand. So whether a kind supported a capability was decided by whether
whoever wrote that lambda remembered to thread it through — and nothing
recorded the answer, in either direction.

That is not a hypothetical. `render_worker` was `render_service` minus
the Service object, and it silently minus'd the RBAC surface too. A
project whose worker needed to read a CR had no declarative path to say
so, and the failure did not surface at render time: the manifests were
valid, the Deployment rolled, the pod started, and the FIRST sweep of
its reconcile loop failed with

    workspaces.reliant.dev is forbidden: User "system:serviceaccount:
    control-plane-prod:control-plane-workers" cannot list resource
    "workspaces" in API group "reliant.dev"

A capability that is absent because nobody thought about it looks
EXACTLY like a capability that is absent on purpose. `cron` gets no
readiness probe deliberately (a CronJob pod runs to completion; probing
it is meaningless), and `worker` got no Role accidentally, and from the
outside the two were the same shrug.

WHAT THIS FILE IS
=================
The matrix, stated ONCE, as data. `KIND_CAPABILITIES` maps each kind to
the capabilities it can express. The authoring schema's checks
(kcl/workload.k) read it: a workload that declares a capability its kind
cannot express is REJECTED at render time, naming the field and the kind,
instead of having the declaration silently dropped on the floor.

The Kubernetes renderer is Go (pkg/deploy.RenderWorkloads, ADR 0002 §4),
and it applies the same kind semantics to the WorkloadSpec. This table is
the AUTHOR-TIME half: it answers "may this kind say that?" at the line
that says it, before any renderer runs. A kind that genuinely cannot do
something SAYS SO, out loud, at the point of declaration.

WHY A MATRIX RATHER THAN A SCHEMA HIERARCHY
===========================================
The obvious alternative is a `SchedulableWorkload` base schema that
each scheduled kind composes, so a new capability is inherited rather
than repeated. It was rejected for the reason schema.k already gives
for having one `Workload` schema with a `kind` discriminator instead of
six subschemas: the kinds share EVERY FIELD and differ only in which
expansion runs. A base + five subschemas would add no fields, and it
would break the one thing users actually do — `wl.api | {replicas = 3}`
type-checks against the same schema in every environment, which is what
lets a per-env refinement re-run the `check:` block on the merged
result.

Inheritance also cannot express the interesting half of this matrix.
"`tool` does not support replicas" is not the absence of an inherited
field; it is a POSITIVE claim that a declaration should be rejected.
A subschema that simply lacks a field cannot reject it — KCL would
report an unknown attribute, if it reported anything, and the error
would name the schema rather than the reason. Here the check says
"kind='tool' cannot declare ports — a tool is never scheduled".

The second alternative — per-kind capability declarations asserted by a
test — is what this IS, with the assertion moved from the test into the
render path, so a gap fails for the project that hits it and not only
for forge's own CI.

READING THE MATRIX
==================
A capability listed for a kind is one that kind can express, and that
the renderer will honor. A capability ABSENT from a kind is a
deliberate claim that the capability is meaningless there — each one is
justified in the comment beside the kind. Absence is never "nobody got
to it": if that is the situation, the entry is missing and this file is
wrong, which is a bug to fix here rather than to work around downstream.
"""

# ---------------------------------------------------------------------------
# Capability names. String constants rather than a bare-string convention so
# a typo is a KCL name error at load, not a capability that silently never
# matches — `supports(kind, "namespced_rbac")` would otherwise return False
# forever and read exactly like a deliberate hole.
# ---------------------------------------------------------------------------

# Renders a pod-bearing manifest at all (Deployment / CronJob / Job).
# `tool` is the sole kind without it.
CAP_WORKLOAD: str = "workload"

# Renders a long-running Deployment (as opposed to a batch object).
CAP_DEPLOYMENT: str = "deployment"

# Renders a k8s Service object, so other workloads can dial it by name.
CAP_SERVICE_OBJECT: str = "service_object"

# Honors `ports` — as containerPorts, and as ServicePorts where the kind
# also has CAP_SERVICE_OBJECT.
CAP_PORTS: str = "ports"

# Honors `replicas` (and, above 1, topology spread).
CAP_REPLICAS: str = "replicas"

# May be probed. A service, worker or operator serves /readyz + /healthz
# through serverkit; a batch pod runs to completion, so probing it is
# meaningless.
CAP_PROBES: str = "probes"

# Gets a PodDisruptionBudget when `replicas > 1`, so a node drain takes one
# replica at a time.
CAP_PDB: str = "pdb"

# Honors `namespaced_rbac` — a Role + RoleBinding in the workload's own
# namespace, on top of the default config-read rules.
CAP_NAMESPACED_RBAC: str = "namespaced_rbac"

# Honors `clusterRBAC` — a ClusterRole + ClusterRoleBinding carrying ONLY
# the declared cluster-scoped rules. Any scheduled kind may declare it (a
# worker that reads nodes, a job that installs a CRD). It ADDS to the
# namespaced tier: the config-read defaults stay in the workload's Role, so a
# ClusterRole never grants cluster-wide read on Secrets. Both tiers bind the
# one ServiceAccount (pkg/deploy identity).
CAP_CLUSTER_RBAC: str = "cluster_rbac"

# Honors `resources` (CPU/memory requests + limits).
CAP_RESOURCES: str = "resources"

# Honors `schedule` — renders a CronJob on a recurring cron expression.
CAP_SCHEDULE: str = "schedule"

# May DECLARE ordering over other workloads via `before`.
CAP_ORDERING: str = "ordering"

# May RECEIVE ordering: a one-shot that gates this kind is lowered onto it
# as an initContainer. The dual of CAP_ORDERING, and deliberately separate —
# `job` is the only kind with both.
CAP_GATED: str = "gated"

# Honors `crds` — the CRD kind names an operator owns.
CAP_CRDS: str = "crds"

# ---------------------------------------------------------------------------
# THE MATRIX.
#
# Every cell of this table is a decision. The comment on each kind names the
# capabilities it deliberately does NOT have and why, because that is the
# half a reader cannot recover from the code.
# ---------------------------------------------------------------------------
KIND_CAPABILITIES: {str: [str]} = {
    # A long-running server other workloads dial by name. The most capable
    # kind, and the one every other scheduled kind is a restriction of.
    #
    # No CAP_ORDERING: a service is what one-shots gate, not a one-shot
    # itself. CAP_CLUSTER_RBAC it has: a service may resolve resources across
    # namespaces (a proxy routing to per-workspace objects) without being a
    # controller.
    "service" = [
        CAP_WORKLOAD, CAP_DEPLOYMENT, CAP_SERVICE_OBJECT, CAP_PORTS,
        CAP_REPLICAS, CAP_PROBES, CAP_PDB, CAP_NAMESPACED_RBAC,
        CAP_CLUSTER_RBAC, CAP_RESOURCES, CAP_GATED,
    ]

    # A long-running process nothing dials — a queue consumer, a reconcile
    # loop, a Temporal worker.
    #
    # No CAP_SERVICE_OBJECT, and THIS is the deliberate one: nothing dials a
    # worker, so a Service object would be an endpoint list nobody resolves.
    # It still keeps CAP_PORTS (a worker built on serverkit binds a port and
    # answers /healthz on it, which is also why it keeps CAP_PROBES) and
    # CAP_PDB (multi-replica workers drain one at a time like any other
    # Deployment).
    #
    # CAP_NAMESPACED_RBAC is the capability whose ABSENCE was the bug this
    # matrix exists to prevent. A worker is the kind MOST likely to need it:
    # it is where reconcile loops and sweepers live, and those read the API
    # server by definition.
    "worker" = [
        CAP_WORKLOAD, CAP_DEPLOYMENT, CAP_PORTS, CAP_REPLICAS, CAP_PROBES,
        CAP_PDB, CAP_NAMESPACED_RBAC, CAP_CLUSTER_RBAC, CAP_RESOURCES, CAP_GATED,
    ]

    # A recurring batch job on a cron schedule.
    #
    # No CAP_REPLICAS: concurrency for a CronJob is `concurrencyPolicy` on
    # the schedule, not a replica count — a CronJob with `replicas = 3` is
    # not three concurrent runs, it is a field k8s has nowhere to put.
    # No CAP_PROBES: the pod runs to completion, so a liveness probe would
    # restart a job that is merely still working, and a readiness probe
    # gates traffic that never arrives. No CAP_PDB: there is nothing
    # long-running to protect from a drain. No CAP_GATED: a CronJob pod
    # fires on a SCHEDULE rather than as part of a release, so an init
    # container there would re-run the one-shot on every tick, forever.
    #
    # CAP_NAMESPACED_RBAC it does have, and did not: a sweeper that reaps
    # stale objects on a schedule is the textbook cron, and reaping means
    # list + delete against the API server.
    "cron" = [
        CAP_WORKLOAD, CAP_SCHEDULE, CAP_NAMESPACED_RBAC, CAP_CLUSTER_RBAC,
        CAP_RESOURCES,
    ]

    # A one-shot that runs to completion, optionally gating others.
    #
    # The only kind with CAP_ORDERING — `before` is what a one-shot is FOR.
    # It also has CAP_GATED, so a seed job cannot beat the migration that
    # creates the table it seeds.
    #
    # No CAP_REPLICAS: a one-shot's parallelism is `spec.parallelism`, and
    # forge's jobs are the run-once kind. No CAP_PROBES / CAP_PDB /
    # CAP_SERVICE_OBJECT for the same reason as cron.
    "job" = [
        CAP_WORKLOAD, CAP_NAMESPACED_RBAC, CAP_CLUSTER_RBAC, CAP_RESOURCES,
        CAP_ORDERING, CAP_GATED,
    ]

    # A controller-runtime manager that watches the API server.
    #
    # CAP_CLUSTER_RBAC AND CAP_NAMESPACED_RBAC. An operator watches its CRDs
    # across namespaces, so those rules are cluster-scoped, but its config
    # read and its leader-election lease are in its OWN namespace, and they
    # go in a namespaced Role. Putting them in the ClusterRole (as this
    # matrix once did, calling the cluster tier a superset) granted every
    # operator read on every Secret in the cluster. A namespace-local grant
    # an operator declares belongs in namespacedRBAC for the same reason.
    #
    # CAP_PORTS it does have, and this cell was got wrong the first time
    # this matrix was written — caught by rendering a real project against
    # it. A controller-runtime manager BINDS ports as a matter of course:
    # :8080 metrics, :8081 health, :9443 for a webhook. `render_operator`
    # passed an empty port list to the Deployment builder, so an operator
    # that declared ports had them silently dropped, which is the same
    # class of defect as the missing RBAC on `worker`. control-plane's
    # workspace-controller declares `port = 9191` and has been getting a
    # containerPort-less manifest for it.
    #
    # No CAP_SERVICE_OBJECT, though, and that asymmetry is deliberate:
    # containerPorts are documentary and always safe to state, whereas a
    # Service is a routable endpoint other workloads resolve by name, and
    # forge should not conjure one for a manager on the strength of a port
    # number. An operator that genuinely wants to be dialed attaches its
    # own Service as an owned manifest. (Consequence worth knowing:
    # `Port.expose` is inert on an operator — see the report accompanying
    # this change, which leaves that gap open deliberately rather than
    # quietly inventing a Service object for every operator in existence.)
    #
    # CAP_PROBES: an operator's manager answers /healthz + /readyz — under
    # serverkit on its PORT (a forge-built operator, which the lowering
    # defaults exactly like a service), or on controller-runtime's own probe
    # listener. An unprobed manager that wedges is otherwise never
    # restarted, and doctor rightly fails the pod for it.
    "operator" = [
        CAP_WORKLOAD, CAP_DEPLOYMENT, CAP_PORTS, CAP_REPLICAS, CAP_PROBES, CAP_PDB,
        CAP_NAMESPACED_RBAC, CAP_CLUSTER_RBAC, CAP_CRDS, CAP_RESOURCES, CAP_GATED,
    ]

    # Built into the image and NEVER scheduled — a CLI, an admin escape
    # hatch you `kubectl exec` to run.
    #
    # THE EMPTY LIST IS THE POINT, and it is the strongest claim in this
    # file. A tool renders no manifest at all, so every capability here is
    # about a pod that does not exist. Ports, replicas, probes, RBAC and
    # resources are not merely ignored for a tool — declaring one means the
    # author believes something schedules it, and the honest response is to
    # reject the declaration and say `kind='service'` instead of rendering
    # a workload that is not there.
    "tool" = []
}

# Every valid kind. Derived from the matrix so a kind cannot exist without
# a capability row — the failure mode where a new kind renders nothing and
# reports no error.
WORKLOAD_KINDS: [str] = [_k for _k in KIND_CAPABILITIES]

# Kinds that render a long-running Deployment.
DEPLOYMENT_KINDS: [str] = [
    _k for _k in KIND_CAPABILITIES if CAP_DEPLOYMENT in KIND_CAPABILITIES[_k]
]

# The kinds a BROADCAST job (`before = ["*"]`) gates — those that RECEIVE
# ordering. Derived rather than restated: the previous hand-maintained list
# and the per-kind expansions were two places one fact was written down, and
# `cron`'s exclusion had to be justified in both.
BROADCAST_GATED_KINDS: [str] = [
    _k for _k in KIND_CAPABILITIES if CAP_GATED in KIND_CAPABILITIES[_k]
]

# The kinds that may carry probes. The DEFAULT probe policy (a forge-built
# service, or operator with ports, gets HTTP /readyz + /healthz) is kcl/workload.k's
# `_default_probes`, and Go's WorkloadSpec.EffectiveProbes applies the rest.
PROBED_KINDS: [str] = [
    _k for _k in KIND_CAPABILITIES if CAP_PROBES in KIND_CAPABILITIES[_k]
]

# Does `kind` support `capability`?
#
# An unknown kind supports nothing, rather than erroring: `Workload.check`
# already rejects an invalid kind with a message naming the valid ones, and
# a second error from here would fire FIRST and report the capability rather
# than the typo that caused it.
supports = lambda kind: str, capability: str -> bool {
    capability in KIND_CAPABILITIES[kind] if kind in KIND_CAPABILITIES else False
}
