"""
Negative test — a host MinIO binds 127.0.0.1, so a pod (forge.OnCluster) cannot reach
its bucket; only a host process can.
"""

# expect: which listens on 127.0.0.1 of this machine

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}

output = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    buckets = [forge.Bucket {name = "uploads", runtime = forge.BucketOnMinIO {infra = "minio"}}]
    infra = [forge.HostInfra {name = "minio", engine = "minio", port = 9200}]
    workloads = [fw.Workload {name = "api", image = _img, env = forge.bucket_env("UPLOADS", "uploads"), runtime = forge.OnCluster {target = _k3d}}]
    secret_provider = forge.FileSecrets {path = "secrets/dev.yaml"}
})
