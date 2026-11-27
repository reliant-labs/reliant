"""
Negative test — a host MinIO's port is its HostInfra's; a second one on the
BucketOnMinIO would be a second source of the same number.
"""

# expect: takes no port or console_port

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    buckets = [forge.Bucket {name = "uploads", runtime = forge.BucketOnMinIO {infra = "minio", port = 9200}}]
    infra = [forge.HostInfra {name = "minio", engine = "minio", port = 9200}]
    secret_provider = forge.FileSecrets {path = "secrets/dev.yaml"}
})
