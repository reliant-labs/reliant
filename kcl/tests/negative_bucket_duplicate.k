"""
Negative test — a bucket name is declared once per env: forge.BucketRef
names one.
"""

# expect: a bucket name is declared twice

import forge

_minio = forge.BucketOnMinIO {port = 9123}
_buckets = [forge.Bucket {name = "uploads", runtime = _minio}, forge.Bucket {name = "uploads", runtime = _minio, public_read = True}]

output = forge.render(forge.Bundle {
    project = "acme"
    buckets = _buckets
    workloads = [forge.minio_workload(_minio, _buckets)]
})
