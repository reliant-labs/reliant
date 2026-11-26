"""
Negative test — KubeRay is adopt-only by policy: an Install of it needs a reason.
"""

# expect: Install of kind 'kuberay' is refused: kuberay (Ray) is adopt-only by policy
# expect: state why forge must install it: reason = "<why this cluster needs it>"

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
        training = [forge.Install {kind = "kuberay", chart = _npd | {name = "kuberay-operator"}}]
    }]
})
