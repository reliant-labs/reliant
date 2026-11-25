"""
vite-spa / staging: a frontend with no backend infrastructure.
YOURS: forge scaffolds this file once. Each frontend binds its own runtime.
Static hosting is managed by Reliant: the bucket, the CDN and the hostname. Hosting limits and billing are enforced by the platform; see `forge skill load deploy/static-site`.
`forge env deploy staging` publishes the static export.
"""
import forge

_hosted_frontend = lambda f: forge.Frontend -> forge.Frontend {
    f | {image = f.name, runtime = forge.OnHosted {}}
}
# Hosting elsewhere is a binding edit: declare your bucket, then rebind
# the frontend's line to `_on_bucket(...)`.
_bucket = None
_on_bucket = lambda f: forge.Frontend -> forge.Frontend {
    assert _bucket, "declare _bucket = forge.OnBucket {bucket = \"your-bucket\"}, or use _hosted_frontend"
    f | {runtime = _bucket}
}
_on_host_frontend = lambda f: forge.Frontend -> forge.Frontend {
    f | {runtime = forge.OnHost {}}
}

# Masked session recording for this environment. It sets the frontend's
# `telemetry.replay` (forge.BrowserTelemetry), which renders its OTEL_REPLAY
# and opts the env in to replay ingestion; False stops both. DNT/GPC and the
# app's consent gate still win.
# Dev uses hosted telemetry when signed in; offline, supply a collector to
# record sessions (observability.local_port), or leave capture unavailable.
_session_replay = False
_web_frontend = forge.Frontend {
    name = "web"
    type = "vite"
    path = "."
    routes = ["none", ]
    public_dir = "dist"
    telemetry = forge.BrowserTelemetry {replay = _session_replay}
}

_bundle = forge.Bundle {
    project = "vite-spa"
    control_plane = forge.ControlPlane {}
    observability = forge.Observability {}
    frontends = [
        _hosted_frontend(_web_frontend)
    ]
    secret_provider = forge.HostedSecrets {}
}
output = forge.render(_bundle)
