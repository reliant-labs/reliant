"""
Positive test — the HOSTED secret provider.

HostedSecrets names no endpoint of its own: values live in the env's
control_plane and are materialized in-cluster by it. The declaration
projects into the ENTITY output as {type: "hosted"} beside control_plane,
and — like every provider — never reaches the manifest stream.
"""

import forge

_bundle = forge.Bundle {
    project = "fixture"
    workloads = []
    control_plane = forge.ControlPlane {
        endpoint = "https://api.example.com"
    }
    secret_provider = forge.HostedSecrets {}
}

output = forge.render(_bundle)
manifests = forge.render(_bundle).manifests

assert_hosted_type = output.secret_provider.type == "hosted"
assert_hosted_no_path = "path" not in output.secret_provider or not output.secret_provider.path
assert_control_plane_rides_along = output.control_plane.endpoint == "https://api.example.com"
assert_no_secret_manifest = len([m for m in manifests if m.kind == "Secret"]) == 0
