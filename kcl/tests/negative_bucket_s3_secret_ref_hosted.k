"""
Negative test — a hosted workload reads a BucketOnS3 credential from the
env's secret store (forge.ManagedSecret), never from a Kubernetes Secret.
"""

# expect: access_key_id is a forge.SecretRef, a Kubernetes Secret a hosted workload cannot read

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
_r2 = forge.BucketOnS3 {
    endpoint = "https://acct.r2.cloudflarestorage.com"
    access_key_id = forge.SecretRef {name = "media-s3", key = "access_key_id"}
    secret_access_key = forge.ManagedSecret {name = "MEDIA_SECRET_ACCESS_KEY"}
}

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    buckets = [forge.Bucket {name = "media", runtime = _r2}]
    workloads = [fw.Workload {name = "web", image = _img, env = forge.bucket_env("MEDIA", "media"), runtime = forge.OnHosted {}}]
})
