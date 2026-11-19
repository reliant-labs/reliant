"""
render contract — the CLUSTER runtime: every scheduled kind, a third-party
image, a cluster ManagedDatabase, the env NetworkPolicy, and the Workload
records in `manifests`.
"""
# kcl-args: env="dev"

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
# The image carries the registry — no env field does. forge appends the tag.
_image = "localhost:5050/acme"
_build = forge.GoBuild {cmd = "./cmd/acme", output_name = "acme"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "dev"
    network_policy = forge.NetworkPolicy {}
    workloads = [
        fw.Workload {
            name = "api"
            image = _image
            build = _build
            args = ["api"]
            replicas = 2
            ports = [fw.Port {name = "http", port = 8080, expose = True}]
            env = {
                LOG_LEVEL = "info"
                API_KEY = forge.SecretRef {name = "acme-secrets", key = "api_key", optional = True}
                POD_NAME = forge.FieldRef {path = "metadata.name"}
            }
            runtime = forge.OnCluster {target = _k3d}
        }
        fw.Workload {name = "migrate", kind = "job", image = _image, build = _build, args = ["db", "migrate", "up"], before = [fw.BEFORE_ALL], runtime = forge.OnCluster {target = _k3d}}
        fw.Workload {
            name = "nightly"
            kind = "cron"
            image = _image
            build = _build
            args = ["sweep"]
            schedule = "@daily"
            namespacedRBAC = [fw.PolicyRule {apiGroups = [""], resources = ["configmaps"], verbs = ["list", "delete"]}]
            runtime = forge.OnCluster {target = _k3d}
        }
        fw.Workload {name = "nats", image = "docker.io/library/nats:2.10", ports = [fw.Port {name = "client", port = 4222}], runtime = forge.OnCluster {target = _k3d}}
        fw.Workload {
            name = "workspace"
            kind = "operator"
            image = _image
            build = _build
            args = ["workspace-controller"]
            crds = ["Workspace"]
            leaderElection = True
            clusterRBAC = [fw.PolicyRule {apiGroups = ["acme.dev"], resources = ["workspaces"], verbs = ["get", "list", "watch"]}]
            runtime = forge.OnCluster {target = _k3d}
        }
    ]
    databases = [forge.ManagedDatabase {name = "db"}]
    secret_provider = forge.FileSecrets {path = "secrets/dev.yaml"}
})
