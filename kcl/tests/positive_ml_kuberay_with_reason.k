"""
Positive test — KubeRay is adopt-only by policy, and an Install of it renders
once the author states why. The reason is projected in output.ml_stacks, so a
reviewer reading the render sees the exception.
"""

import forge

_gpu = forge.ClusterTarget {cluster = "gke_acme_us-central1_gpu", namespace = "forge-ml"}

_out = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    cluster_target = _gpu
    ml_stacks = [forge.MLStack {
        cluster = _gpu
        training = [forge.Install {
            kind = "kuberay"
            reason = "the research team's RayJobs need a KubeRay operator and this cluster runs none"
            chart = forge.HelmChart {
                name = "kuberay-operator", repo = "https://ray-project.github.io/kuberay-helm/"
                chart = "kuberay-operator", version = "1.4.2", namespace = "forge-ml", delivery = "bundle"
            }
        }]
    }]
})

assert_chart_rendered = [c.name for c in _out.bundled_charts] == ["kuberay-operator"]
assert_chart_labelled = _out.bundled_charts[0].labels["forge.dev/component"] == "ml-kuberay"
assert_reason_projected = _out.ml_stacks[0].components[0].reason == "the research team's RayJobs need a KubeRay operator and this cluster runs none"
assert_mode = _out.ml_stacks[0].components[0].mode == "install" and _out.ml_stacks[0].components[0].slot == "training"
