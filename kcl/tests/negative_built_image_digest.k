"""
Negative test — a forge-BUILT workload cannot pin its image to a digest.

A digest names bytes that already exist; no build can produce a digest
chosen in advance. Rendering it anyway leaves two wrong options: build to
some tag the deploy (pinned to the digest) never pulls, or skip the declared
build. Either way the declaration lies, so the render refuses, naming the
workload. `forge env deploy` pins what the build pushed by digest on its own.
A digest on a workload forge does NOT build is fine (positive_image_tag_pin.k).
"""

# expect: workload 'api'
# expect: pins a digest, but the workload also declares a build

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "dev"
    workloads = [_w | {runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-dev", namespace = "dev"}}} if not _w.runtime else _w for _w in [fw.Workload {
        name = "api"
        image = "localhost:5050/reliant@sha256:abc123def456abc123def456abc123def456abc123def456abc123def456abcd"
        args = ["api"]
        build = forge.ShellBuild {cmd = "true"}
    }]]
})
