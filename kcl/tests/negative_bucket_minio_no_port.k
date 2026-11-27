"""
Negative test — the compose MinIO publishes its S3 API on BucketOnMinIO.port, so a
BucketOnMinIO with neither a port nor a host `infra` is refused.
"""

# expect: BucketOnMinIO.port is required for the compose MinIO

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    buckets = [forge.Bucket {name = "uploads", runtime = forge.BucketOnMinIO {}}]
})
