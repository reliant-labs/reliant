"""
Negative test — a bucket name follows S3's naming rule: 3..63 lowercase
letters, digits and hyphens.
"""

# expect: Bucket.name 'Uploads_1' must be 3..63 lowercase letters

import forge

_minio = forge.BucketOnMinIO {port = 9123}
_buckets = [forge.Bucket {name = "Uploads_1", runtime = _minio}]

output = forge.render(forge.Bundle {
    project = "acme"
    buckets = _buckets
    workloads = [forge.minio_workload(_minio, _buckets)]
})
