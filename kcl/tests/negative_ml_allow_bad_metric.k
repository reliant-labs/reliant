"""
Negative test — an allow entry is a Prometheus metric name (they are joined into the scrape regex).
"""

# expect: which are not Prometheus metric names

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
        accelerators = [forge.Accelerators {vendor = "nvidia", metrics = forge.Adopt {kind = "dcgm-exporter", selector = {"app": "nvidia-dcgm-exporter"}, port = 9400, allow = ["DCGM_FI_.*"]}}]
    }]
})
