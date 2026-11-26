"""ML agent placement participates in lifecycle and single-cluster Flux checks."""
# kcl-args: policy=local
# kcl-args: policy=ephemeral
# kcl-args: policy=flux
# expect: gke_acme_us-central1_gpu
import forge

_policy = option("policy", default="local")
output = forge.render(forge.Bundle {
    project = "acme"
    env = "dev"
    lifecycle = _policy if _policy != "flux" else Undefined
    applier = "flux" if _policy == "flux" else Undefined
    cluster_target = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
    observability = forge.Observability {
        destination = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}
    }
    ml_stacks = [forge.MLStack {
        cluster = forge.ClusterTarget {cluster = "gke_acme_us-central1_gpu", namespace = "forge-ml"}
        monitoring = [forge.Adopt {kind = "prometheus", namespace = "monitoring", selector = {app = "prometheus"}, port = 9090}]
    }]
})
