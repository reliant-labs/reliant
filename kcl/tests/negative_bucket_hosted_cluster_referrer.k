"""
Negative test — a workload on a cluster cannot read a hosted bucket: its
credential Secret only exists in the control plane's namespace.
"""

# expect: is on forge.OnHosted, whose credential only exists in the control plane's namespace
# expect: cluster runtime

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    buckets = [forge.Bucket {name = "uploads", runtime = forge.OnHosted {}}]
    workloads = [fw.Workload {
        name = "worker"
        kind = "worker"
        image = _img
        env = forge.bucket_env("UPLOADS", "uploads")
        runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}}
    }]
})
