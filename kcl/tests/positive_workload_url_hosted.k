"""
Positive test — forge.WorkloadURL from a HOSTED workload and a HOSTED
frontend (forge.OnHosted) is KEPT as a reference, for the control plane to
resolve.

The platform allocates hostnames, so nothing in the render knows the URL;
the reference is carried into the published spec unchanged —
StaticSite runtimeConfig.<KEY>.workloadURL and Workload env[].workloadURL —
and the control plane resolves it, and re-resolves when the target moves.
Literals still pass through as {value}.

Per REFERRER now (map §6.3): a hosted referrer keeps refs; a host/cluster
referrer in the same env resolves at render
(positive_workload_url_cross_runtime.k).
"""

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

_out = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    workloads = [fw.Workload {
        name = "api"
        image = _img
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        env = {CORS_ORIGINS = forge.WorkloadURL {workload = "web"}}
        runtime = forge.OnHosted {}
    }]
    frontends = [forge.Frontend {
        name = "web"
        image = "ghcr.io/acme/web"
        path = "frontends/web"
        type = "vite"
        runtime_config = {
            API_URL = forge.WorkloadURL {workload = "api"}
            APP_NAME = "acme"
        }
        public_dir = "dist"
        runtime = forge.OnHosted {}
    }]
})

_fe = _out.frontends[0]
assert_ref_kept = _fe.runtime_config_spec.API_URL == {"workloadURL": {"name": "api"}}
assert_literal_wire = _fe.runtime_config_spec.APP_NAME == {"value": "acme"}
assert_nothing_resolved = len(_fe.runtime_config) == 0

_env = _out.workloads[0].spec.env[0]
assert_env_ref_kept = _env.workloadURL.name == "web"
assert_env_no_value = "value" not in _env
assert_no_records = len([m for m in _out.manifests if m.kind == "Workload"]) == 0
