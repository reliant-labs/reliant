"""
Positive test — every type an fw.Workload field is typed with is nameable
through `forge.workloads` alone: a pod-identity override (fw.PodSecurity)
and a sidecar env var with a value source (fw.EnvVar / fw.SecretKeyRef)
need no second `import forge.tiers`.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    cluster_target = _k3d
    workloads = [fw.Workload {
        name = "api"
        image = "localhost:5050/demo"
        runtime = forge.OnCluster {target = _k3d}
        ports = [fw.Port {name = "http", port = 8080}]
        securityContext = fw.PodSecurity {runAsUser = 1000, runAsGroup = 1000, fsGroup = 1000}
        sidecars = [fw.Container {
            name = "proxy"
            image = "gcr.io/cloud-sql-connectors/cloud-sql-proxy:2.14.1"
            env = [fw.EnvVar {name = "TOKEN", secretRef = fw.SecretKeyRef {name = "s", key = "k"}}]
        }]
    }]
})

_spec = output.workloads[0].spec
assert_security_context = _spec.securityContext == {fsGroup = 1000, runAsGroup = 1000, runAsUser = 1000}
assert_sidecar_env = _spec.sidecars[0].env[0].secretRef.name == "s"
