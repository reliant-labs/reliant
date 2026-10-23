"""
Positive test — frontends mix runtimes exactly as workloads do.

`hosted_env`: a HOSTED frontend next to CLUSTER workloads. control_plane is
required because the frontend is hosted, even though no workload is.

`bucket_env`: a BUCKET frontend next to HOSTED workloads, and a dev-served
frontend in the same env. The bucket frontend is the author's own; nothing
about the env's hosted workloads makes it hosted.
"""

import forge
import forge.workloads as fw

_k8s = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme"}

hosted_env = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api:v1", ports = [fw.Port {name = "http", port = 8080}], runtime = forge.OnCluster {target = _k8s}}]
    frontends = [forge.Frontend {name = "web", image = "ghcr.io/acme/web", path = "web", type = "vite", runtime = forge.OnHosted {}}]
})

bucket_env = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api:v1", ports = [fw.Port {name = "http", port = 8080, expose = True}], runtime = forge.OnHosted {}}]
    frontends = [
        forge.Frontend {name = "web", path = "web", type = "vite", runtime = forge.OnBucket {bucket = "acme-web"}}
        forge.Frontend {name = "docs", path = "docs", type = "vite", port = 5174, runtime = forge.OnHost {}}
    ]
})

assert_hosted_frontend = hosted_env.frontends[0].runtime.type == "hosted"
assert_cluster_workload = hosted_env.workloads[0].runtime.type == "cluster"
assert_cluster_record = len([m for m in hosted_env.manifests if m.kind == "Workload"]) == 1

assert_bucket_frontend = bucket_env.frontends[0].runtime.type == "bucket"
assert_host_frontend = bucket_env.frontends[1].runtime.type == "host"
assert_hosted_workload = bucket_env.workloads[0].runtime.type == "hosted"
