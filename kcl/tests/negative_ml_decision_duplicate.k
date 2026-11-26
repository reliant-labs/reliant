"""
Negative test — a rule is decided once per stack.
"""

# expect: decides recommendation [kubelet-tracing-off] twice

import forge

_gpu = forge.ClusterTarget {cluster = "gke_acme_us-central1_gpu", namespace = "forge-ml"}
_npd = forge.HelmChart {name = "node-problem-detector", repo = "https://charts.deliveryhero.io/", chart = "node-problem-detector", version = "2.3.14", namespace = "forge-ml", delivery = "bundle"}
_dcgm_chart = forge.HelmChart {name = "dcgm-exporter", repo = "https://nvidia.github.io/dcgm-exporter/helm-charts", chart = "dcgm-exporter", version = "4.1.1", namespace = "forge-ml", delivery = "bundle"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    cluster_target = _gpu
    ml_stacks = [forge.MLStack {
        cluster = _gpu
        decisions = [
            forge.RecommendationDecision {id = "kubelet-tracing-off", reason = "GKE hides it", expires = "2027-06-01"}
            forge.RecommendationDecision {id = "kubelet-tracing-off", reason = "still hidden", expires = "2027-12-01"}
        ]
    }]
})
