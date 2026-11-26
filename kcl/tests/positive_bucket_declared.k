"""
Positive test — every forge.Bucket reaches the render's `declared_buckets`,
whatever its runtime, in release.ShapeBucket's vocabulary: the name, the
runtime kind, public_read when set, and for a BucketOnS3 its real bucket (the
declared one, else the name) and endpoint. Never a credential. `buckets`
stays the hosted ones the platform provisions. An env with no bucket renders
no key.
"""

import forge
import forge.workloads as fw

_r2 = forge.BucketOnS3 {
    endpoint = "https://acct.r2.cloudflarestorage.com/"
    region = "auto"
    bucket = "acme-prod-media"
    access_key_id = forge.ManagedSecret {name = "MEDIA_ACCESS_KEY_ID"}
    secret_access_key = forge.ManagedSecret {name = "MEDIA_SECRET_ACCESS_KEY"}
}
_own = forge.BucketOnS3 {
    endpoint = "http://minio.storage.svc:9000"
    access_key_id = forge.SecretRef {name = "archive-s3", key = "access_key_id"}
    secret_access_key = forge.SecretRef {name = "archive-s3", key = "secret_access_key"}
}
_web = fw.Workload {name = "web", image = "ghcr.io/acme/web", runtime = forge.OnHosted {}}
_cp = forge.ControlPlane {endpoint = "https://api.example.com"}

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = _cp
    buckets = [
        forge.Bucket {name = "uploads", runtime = forge.OnHosted {}, public_read = True}
        forge.Bucket {name = "media", runtime = _r2}
        forge.Bucket {name = "archive", runtime = _own}
    ]
    workloads = [_web]
})
_minio = forge.BucketOnMinIO {port = 9123}
_scratch = [forge.Bucket {name = "scratch", runtime = _minio}]
_dev = forge.render(forge.Bundle {project = "acme", lifecycle = "local", buckets = _scratch, workloads = [forge.minio_workload(_minio, _scratch)]})
_bare = forge.render(forge.Bundle {project = "acme", control_plane = _cp, workloads = [_web]})

_d = {b.name: b for b in output.declared_buckets}

assert_every_bucket = [b.name for b in output.declared_buckets] == ["uploads", "media", "archive"]
assert_hosted_by_name_alone = _d.uploads == {"name": "uploads", "runtime_kind": "hosted", "public_read": True}
assert_s3_real_name_and_endpoint = _d.media == {"name": "media", "runtime_kind": "s3", "bucket": "acme-prod-media", "endpoint": "https://acct.r2.cloudflarestorage.com"}
assert_s3_bucket_defaults_to_name = _d.archive == {"name": "archive", "runtime_kind": "s3", "bucket": "archive", "endpoint": "http://minio.storage.svc:9000"}
assert_minio_by_name_alone = _dev.declared_buckets == [{"name": "scratch", "runtime_kind": "minio"}] and "buckets" not in _dev
assert_no_credential = all b in output.declared_buckets { "access_key_id" not in b and "secret_access_key" not in b }
assert_buckets_stay_the_hosted_ones = [b.name for b in output.buckets] == ["uploads"]
assert_no_bucket_no_key = "declared_buckets" not in _bare
