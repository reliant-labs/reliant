"""
Positive test — `forge.ControlPlane {}` IS Reliant cloud.

The endpoint defaults to forge.RELIANT_CLOUD_ENDPOINT, Reliant's production
control plane, so a plain `go install`'d forge targets prod with nothing but
the empty declaration. Every other control plane (a self-run one, a local dev
stack) is named explicitly with `endpoint`, and that explicit value wins.

The literal URL is pinned here on purpose: changing which server every forge
user deploys to must be a reviewed diff, not a side effect of editing the
constant. internal/cli's TestControlPlaneDefault_KCLMatchesGo pins the Go copy
(internal/cloud.DefaultEndpoint) to the same value.
"""

import forge

_reliant_cloud = forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    secret_provider = forge.HostedSecrets {}
    workloads = []
}

_self_run = forge.Bundle {
    project = "acme"
    env = "dev"
    control_plane = forge.ControlPlane {endpoint = "http://127.0.0.1:8090"}
    workloads = []
}

_cloud_out = forge.render(_reliant_cloud)
_self_out = forge.render(_self_run)

assert_default_is_reliant_cloud = _cloud_out.control_plane.endpoint == "https://admin.reliantapi.com"
assert_default_is_the_named_constant = _cloud_out.control_plane.endpoint == forge.RELIANT_CLOUD_ENDPOINT
assert_default_token_env = _cloud_out.control_plane.token_env == "FORGE_CONTROL_PLANE_TOKEN"
assert_hosted_secrets_satisfied = _cloud_out.secret_provider.type == "hosted"
assert_explicit_endpoint_wins = _self_out.control_plane.endpoint == "http://127.0.0.1:8090"
