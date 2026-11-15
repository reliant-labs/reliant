"""
render contract — a self-hosted env (`Bundle.applier = "flux"`): no control
plane, one cluster, reconciled by a Flux forge installs there. Pins the
resolved `flux_delivery` the decoder reads (the env's one cluster, its
delivery namespace and ResourceSet, the copied pull Secret, signing and
flux_verify resolved to off) and the one bootstrap chart, `flux-delivery`, its render adds beside
the declared ones.
"""
# kcl-args: env="prod"

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {
    cluster = "gke_acme_prod"
    namespace = "acme-prod"
    platform = "amd64"
}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    cluster_target = _prod
    applier = "flux"
    flux_delivery = forge.FluxDelivery {pull_secret_from = "acme-prod/ghcr-pull"}
    workloads = [
        fw.Workload {
            name = "api"
            image = "ghcr.io/acme/acme"
            build = forge.GoBuild {cmd = "./cmd/acme", output_name = "acme"}
            args = ["api"]
            ports = [fw.Port {name = "http", port = 8080, expose = True}]
            runtime = forge.OnCluster {target = _prod}
        }
    ]
})
