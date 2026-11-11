"""
Positive test — `Port.domains` survives on forge.OnCluster, which is the one
runtime that still declares a custom hostname in spec.

On a cluster YOU own the ingress: forge renders the Gateway and the
HTTPRoute, and forge.WorkloadURL resolves an exposed port's first domain as
the workload's URL (kcl/lib/workload_url.k). Nothing asynchronous is waiting
on a registrar, so the name IS spec and converges with the deploy.

The hosted half is the opposite and is refused
(negative_hosted_workload_port_domains.k): there the platform verifies
ownership and issues the certificate, so the domain is a control-plane
resource bound per environment.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-hounders", namespace = "hounders-prod"}

output = forge.render(forge.Bundle {
    project = "hounders"
    workloads = [
        fw.Workload {
            name = "membership"
            image = "ghcr.io/hounders/membership:v1"
            ports = [fw.Port {name = "http", port = 8080, expose = True, domains = ["api.hounders.club", "www.api.hounders.club"]}]
            runtime = forge.OnCluster {target = _k3d}
        }
        # A consumer, so the reference is actually resolved by the render.
        fw.Workload {
            name = "worker"
            kind = "worker"
            image = "ghcr.io/hounders/worker:v1"
            env = {API_URL = forge.WorkloadURL {workload = "membership"}}
            runtime = forge.OnCluster {target = _k3d}
        }
    ]
})

_spec = {w.name: w.spec for w in output.workloads}
assert_port_domains = _spec["membership"].ports[0].domains == ["api.hounders.club", "www.api.hounders.club"]

# The reference resolves to the FIRST domain over https — the reason the
# field is still spec on a cluster at all.
assert_workload_url = [e.value for e in _spec["worker"].env if e.name == "API_URL"] == ["https://api.hounders.club"]
