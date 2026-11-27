"""
Negative test — a host MinIO's root credentials are generated into the env's secret
file, so the env must declare forge.FileSecrets.
"""

# expect: Declare `secret_provider = forge.FileSecrets

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    buckets = [forge.Bucket {name = "uploads", runtime = forge.BucketOnMinIO {infra = "minio"}}]
    infra = [forge.HostInfra {name = "minio", engine = "minio", port = 9200}]
})
