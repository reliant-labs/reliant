"""
Forge KCL — the workload authoring surface, `forge.workloads` (fw).

A WORKLOAD is one deployable unit: an executable, the args that select what
it does, and where it runs. It is NOT a proto service: one binary serving a
dozen Connect services is ONE workload (ADR 0002).

    import forge
    import forge.workloads as fw

    api = fw.Workload {
        name = "api"
        build = forge.GoBuild {cmd = "./cmd/acme", output_name = "acme"}
        args = ["api"]
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
    }

    # an env binds EACH workload to a runtime with the same `|` it uses for
    # any refinement; there is no env-level default
    output = forge.render(forge.Bundle {
        project = "acme"
        workloads = [
            api | {runtime = forge.OnHost {runner = "air"}}
            wl.search | {runtime = forge.OnCluster {target = _k3d}}
        ]
    })

WHY THE SCHEMA LIVES IN THE ROOT `forge` PACKAGE
================================================
`fw.Workload` is `forge.Workload` (kcl/workload.k), re-exported. The
renderer (`forge.render`) and the runtime and build unions (`forge.OnHost`,
`forge.GoBuild`, ...) are root-package declarations, and the renderer types
its input as the authoring schema. A `forge.workloads` -> `forge` schema
dependency the other way round would be the package cycle KCL refuses. So
the schema is declared beside its consumer and re-exported here, under the
name every project already imports. It is the SAME schema, not a copy:
`schema X(fw.Workload)`, `[fw.Workload]` annotations and `|` refinements all
type-check against it.

The generated spec sub-types (`fw.Port`, `fw.Resources`, `fw.Probes`, ...)
are re-exported the same way from `forge.tiers`, so an author never needs to
know which package generated a field's type.
"""

import forge
import forge.tiers
import forge.lib.capabilities as caps

# The one authoring schema (kcl/workload.k).
Workload = forge.Workload
# Its continuous-profiling override (Workload.profiling).
Profiling = forge.Profiling

# The kind x capability matrix (kcl/lib/capabilities.k), re-exported so
# `fw.supports(kind, fw.CAP_...)` reads the table the schema checks read.
KIND_CAPABILITIES = caps.KIND_CAPABILITIES
WORKLOAD_KINDS = caps.WORKLOAD_KINDS
DEPLOYMENT_KINDS = caps.DEPLOYMENT_KINDS
BROADCAST_GATED_KINDS = caps.BROADCAST_GATED_KINDS
PROBED_KINDS = caps.PROBED_KINDS
CAP_WORKLOAD = caps.CAP_WORKLOAD
CAP_DEPLOYMENT = caps.CAP_DEPLOYMENT
CAP_SERVICE_OBJECT = caps.CAP_SERVICE_OBJECT
CAP_PORTS = caps.CAP_PORTS
CAP_REPLICAS = caps.CAP_REPLICAS
CAP_PROBES = caps.CAP_PROBES
CAP_PDB = caps.CAP_PDB
CAP_NAMESPACED_RBAC = caps.CAP_NAMESPACED_RBAC
CAP_CLUSTER_RBAC = caps.CAP_CLUSTER_RBAC
CAP_RESOURCES = caps.CAP_RESOURCES
CAP_SCHEDULE = caps.CAP_SCHEDULE
CAP_ORDERING = caps.CAP_ORDERING
CAP_GATED = caps.CAP_GATED
CAP_CRDS = caps.CAP_CRDS
supports = caps.supports

# The generated WorkloadSpec sub-types (kcl/tiers/tiers_gen.k), with the
# camelCase field names the wire carries.
Port = tiers.Port
Resources = tiers.Resources
Probes = tiers.Probes
PolicyRule = tiers.PolicyRule
Container = tiers.Container
Volume = tiers.Volume
VolumeSource = tiers.VolumeSource
SecretVolumeSource = tiers.SecretVolumeSource
ConfigMapVolumeSource = tiers.ConfigMapVolumeSource
EmptyDirVolumeSource = tiers.EmptyDirVolumeSource
PVCVolumeSource = tiers.PVCVolumeSource
KeyToPath = tiers.KeyToPath
Toleration = tiers.Toleration
PodSecurity = tiers.PodSecurity
# A sidecar's (Container.env) env var and its value sources.
EnvVar = tiers.EnvVar
SecretKeyRef = tiers.SecretKeyRef
ConfigMapKeyRef = tiers.ConfigMapKeyRef
FieldRef = tiers.FieldRef
ManagedSecretRef = tiers.ManagedSecretRef
DatabaseRef = tiers.DatabaseRef
WorkloadURLRef = tiers.WorkloadURLRef

# The BROADCAST selector for `Workload.before` (v1alpha1.BeforeAll): gate
# every workload in the environment without naming any of them. Schema
# migration is the case it exists for, because an enumerated list of
# dependents goes stale the day a workload is added.
BEFORE_ALL: str = forge.BEFORE_ALL
