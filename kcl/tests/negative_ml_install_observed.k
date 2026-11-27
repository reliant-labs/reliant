"""
Negative test — discovery found dcgm-exporter running, so installing a second one is
refused: two DCGM exporters on one node break each other's profiling counters.
"""

# expect: discovery found [dcgm-exporter] running (observed); adopt it with forge.Adopt
# expect: two DCGM exporters on one node break each other's profiling counters

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
        observed = ["gpu-operator", "dcgm-exporter"]
        accelerators = [forge.Accelerators {vendor = "nvidia", metrics = forge.Install {kind = "dcgm-exporter", chart = _dcgm_chart}}]
    }]
})
