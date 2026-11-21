"""
Negative test — a workload on a cluster cannot read a hosted forge.LLM: its
per-env key only exists where the platform runs the workload.
"""

# expect: is on forge.OnHosted, Reliant's gateway
# expect: cluster runtime

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    llm = forge.LLM {runtime = forge.OnHosted {}}
    workloads = [fw.Workload {
        name = "worker"
        kind = "worker"
        image = _img
        env = forge.llm_env()
        runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}}
    }]
})
