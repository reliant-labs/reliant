"""
Positive test — a LOCAL env: host + compose workloads, with a control plane
declared only as the SECRET STORE (HostedSecrets).

`control_plane` is no longer an env mode (ADR 0002 §2). Declaring one does
not make anything hosted: every workload here is host- or compose-bound, so
nothing is published and nothing is a hosted tier. It is exactly the shape
`forge env up` runs against a local control-plane stack.
"""

import forge
import forge.workloads as fw

_bundle = forge.Bundle {
    project = "hounders"
    env = "dev"
    control_plane = forge.ControlPlane {endpoint = "http://127.0.0.1:8090"}
    secret_provider = forge.HostedSecrets {}
    workloads = [_w | {runtime = forge.OnHost {runner = "go-run"}} if not _w.runtime else _w for _w in [
        fw.Workload {
            name = "api"
            build = forge.GoBuild {cmd = "./cmd/hounders"}
            args = ["api"]
            env = {STRIPE_SECRET_KEY = forge.SecretRef {name = "app-secrets", key = "STRIPE_SECRET_KEY"}}
        }
        fw.Workload {name = "postgres", runtime = forge.OnCompose {service = "postgres"}}
    ]]
}

output = forge.render(_bundle)
_w = {w.name: w for w in output.workloads}

assert_control_plane = output.control_plane.endpoint == "http://127.0.0.1:8090"
assert_hosted_provider = output.secret_provider.type == "hosted"
assert_host_workload = _w["api"].runtime.type == "host"
assert_host_secret_ref = [e for e in _w["api"].spec.env if e.name == "STRIPE_SECRET_KEY"][0].secretRef.name == "app-secrets"
assert_compose_workload = _w["postgres"].runtime.type == "compose"
assert_nothing_hosted = len([w for w in output.workloads if w.runtime.type == "hosted"]) == 0
assert_no_secret_manifest = len([m for m in output.manifests if m.kind == "Secret"]) == 0
assert_no_records = len([m for m in output.manifests if m.kind == "Workload"]) == 0
