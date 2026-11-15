"""
Positive test — the ENV-WIDE NetworkPolicy bundle is declared on the Bundle
and rendered by Go.

The gap this pinned closed (kcl/lib/netpol.k): `default-deny` on
Ingress+Egress with allows for DNS and same-namespace traffic ONLY
blackholed a forge app's managed Postgres (5432, outside the cluster), its
OTLP collector (another namespace) and HTTPS, and named an ingress namespace
(`ingress-nginx`) no forge cluster runs.

The bundle is a fact about the NAMESPACE, and RenderWorkloads is the one
call that sees a whole env's set in one namespace, so it renders there
(pkg/deploy Context.Network, EnvNetworkPolicy; golden `netpol`). The KCL
half, pinned here: the env declares egress ports / telemetry / ingress
namespaces ONCE, they reach the contract for the CLI to pass through, and
KCL renders no NetworkPolicy of its own (one renderer).
"""

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke-prod", namespace = "demo-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "demo"
    network_policy = forge.NetworkPolicy {
        egress_ports = [443, 5432]
        telemetry_namespace = "observability"
        ingress_namespace = "envoy-gateway-system"
    }
    workloads = [_w | {runtime = forge.OnCluster {target = _prod}} if not _w.runtime else _w for _w in [fw.Workload {name = "api", image = "ghcr.io/demo/demo", ports = [fw.Port {name = "http", port = 8080, expose = True}]}]]
})

assert_declared_egress = output.network_policy.egress_ports == [443, 5432]
assert_declared_telemetry = output.network_policy.telemetry_namespace == "observability"
assert_declared_ingress = output.network_policy.ingress_namespace == "envoy-gateway-system"
assert_no_kcl_network_policies = len([m for m in output.manifests if m.kind == "NetworkPolicy"]) == 0

# An env that declares none gets no env bundle (Context.Network nil).
_bare = forge.render(forge.Bundle {project = "demo", cluster_target = _prod, workloads = []})
assert_undeclared_is_absent = "network_policy" not in _bare or _bare.network_policy == None
