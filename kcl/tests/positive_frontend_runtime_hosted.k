"""
Positive test — a frontend bound to forge.OnHosted: platform static hosting.

The declaration is exactly control-plane's hosted e2e fixture: the static
build facts on the frontend (`public_dir`), and `runtime = forge.OnHosted {}`
— the same schema a workload binds. There is no bucket to state and nothing
to infer: the control plane owns the bucket and the CDN.

References in runtime_config are KEPT for the platform to resolve, and the
resolved document is empty (the platform writes config.js).
"""

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "fixture"
    control_plane = forge.ControlPlane {}
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api:v1"
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        runtime = forge.OnHosted {}
    }]
    frontends = [forge.Frontend {
        name = "site"
        image = "ghcr.io/acme/site"
        path = "site"
        type = "vite"
        public_dir = "dist"
        runtime_config = {API_URL = forge.WorkloadURL {workload = "api"}}
        runtime = forge.OnHosted {}
    }]
})

_f = output.frontends[0]
assert_runtime_hosted = _f.runtime.type == "hosted"
assert_public_dir = _f.public_dir == "dist"
assert_ref_kept = _f.runtime_config_spec.API_URL == {"workloadURL": {"name": "api"}}
assert_nothing_resolved = len(_f.runtime_config) == 0
# A hosted frontend renders no Kubernetes object: the platform owns it.
assert_no_manifest = len([m for m in output.manifests if m.kind in ["StaticSite", "Deployment"]]) == 0
