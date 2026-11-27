"""
Positive test — a host MinIO whose bucket NO workload references still has
its root credentials in the rendered FileSecrets `generate`, so `forge env up`
mints them: the server reads them itself, whether or not an app does.
"""

import forge

output = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    buckets = [forge.Bucket {name = "gtm-assets", runtime = forge.BucketOnMinIO {infra = "blobs"}}]
    infra = [forge.HostInfra {name = "blobs", engine = "minio", port = 9200}]
    secret_provider = forge.FileSecrets {path = "secrets/dev.yaml"}
})

_gen = output.secret_provider.generate
_blobs = [i for i in output.infra if i.name == "blobs"][0]

assert_no_workload_references_it = len(output.workloads) == 0
assert_generates_user = _gen["BLOBS_ROOT_USER"] == {bytes = 16, encoding = "hex"}
assert_generates_password = _gen["BLOBS_ROOT_PASSWORD"] == {bytes = 32, encoding = "hex"}
assert_infra_names_the_same_keys = _blobs.minio_root_user_secret == "BLOBS_ROOT_USER" and _blobs.minio_root_password_secret == "BLOBS_ROOT_PASSWORD"
assert_bucket_still_served = _blobs.minio_buckets == [{name = "gtm-assets", public_read = False}]
