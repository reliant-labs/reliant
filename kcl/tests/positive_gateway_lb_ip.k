"""
Positive test — pinning a Gateway to a reserved REGIONAL static IP must
go through an EnvoyProxy `loadBalancerIP`, NOT Gateway API
`spec.addresses`.

WHY. Envoy Gateway translates every `spec.addresses` entry of type
IPAddress into the backing Service's `spec.externalIPs`
(internal/gatewayapi/address.go -> infra.Proxy.Addresses ->
resource_provider.go `serviceSpec.ExternalIPs = r.infra.Addresses`).
GKE enables the `DenyServiceExternalIPs` admission plugin, which rejects
any Service carrying spec.externalIPs:

    services "envoy-...-public-852a88bc" is forbidden:
    Use of external IPs is denied by admission control

The Gateway keeps serving, because the Service already exists with the
reserved IP attached out of band — but EG can never update that Service
again, so a newly declared listener port silently never takes effect.
The failure is invisible until the day you add a port.

THE SUPPORTED SHAPE. EG models the GCP case as
`EnvoyProxy.spec.provider.kubernetes.envoyService.loadBalancerIP`, which
lands on the Service as `spec.loadBalancerIP` — the field GKE actually
reads to attach a reserved regional address, and which admission
permits. The EnvoyProxy is bound to one Gateway through
`spec.infrastructure.parametersRef` (EG resolves it in
processGatewayParamsRef; the CR must be in the Gateway's namespace).

`type = "NamedAddress"` is unaffected: that is the GKE *cloud* Gateway
controller's extension naming a reserved GLOBAL reservation, it is not
an IP literal, and EG's address translator ignores every non-IPAddress
type. So it stays in spec.addresses.
"""

import forge
import forge.workloads as fw

_target = forge.ClusterTarget {
    cluster = "gke-example"
    namespace = "example-prod"
}

_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _target
    workloads = [
        fw.Workload {
            name = "api"
            image = "localhost:5050/example"
            ports = [fw.Port {name = "http", port = 8080}]
            runtime = forge.OnCluster {target = _target}
        }
    ]
    gateways = [
        # Pinned to a literal reserved REGIONAL IP — the prod shape.
        forge.Gateway {
            name = "public"
            host = "api.example.com"
            listeners = [
                forge.GatewayListener {name = "http", port = 80, protocol = "HTTP"}
            ]
            addresses = [
                forge.GatewayAddress {type = "IPAddress", value = "34.63.203.181"}
            ]
        },
        # Pinned by GLOBAL reservation NAME — unchanged, stays in
        # spec.addresses, emits no EnvoyProxy.
        forge.Gateway {
            name = "named"
            host = "named.example.com"
            listeners = [
                forge.GatewayListener {name = "http", port = 18080, protocol = "HTTP"}
            ]
            addresses = [
                forge.GatewayAddress {type = "NamedAddress", value = "my-reserved-ip"}
            ]
        },
        # Unpinned — no addresses, no EnvoyProxy, nothing to churn.
        forge.Gateway {
            name = "internal"
            host = "internal.example.com"
            listeners = [
                forge.GatewayListener {name = "http", port = 18081, protocol = "HTTP"}
            ]
        }
    ]
}

manifests = forge.render(_bundle).manifests

_gateways = [m for m in manifests if m.kind == "Gateway"]
_proxies = [m for m in manifests if m.kind == "EnvoyProxy"]

_public_gw = [g for g in _gateways if g.metadata.name == "public"][0]
_named_gw = [g for g in _gateways if g.metadata.name == "named"][0]
_internal_gw = [g for g in _gateways if g.metadata.name == "internal"][0]

# ── The regression this fixture exists to pin ──────────────────────

# An IPAddress-pinned Gateway must NOT carry spec.addresses. That is the
# single line that becomes spec.externalIPs and gets the Service
# rejected by GKE admission on every reconcile.
assert_ip_gw_no_addresses = not _public_gw.spec.get("addresses")

# It pins through an EnvoyProxy instead, bound by parametersRef.
assert_ip_gw_params_group = _public_gw.spec.infrastructure.parametersRef.group == "gateway.envoyproxy.io"
assert_ip_gw_params_kind = _public_gw.spec.infrastructure.parametersRef.kind == "EnvoyProxy"
assert_ip_gw_params_name = _public_gw.spec.infrastructure.parametersRef.name == "public-lb-ip"

# Exactly one EnvoyProxy, in the GATEWAY's namespace (EG resolves
# parametersRef namespace-locally — a CR elsewhere is never found), and
# it carries the literal IP as loadBalancerIP.
assert_proxy_count = len(_proxies) == 1
_proxy = _proxies[0]
assert_proxy_name = _proxy.metadata.name == "public-lb-ip"
assert_proxy_namespace = _proxy.metadata.namespace == "example-prod"
assert_proxy_api = _proxy.apiVersion == "gateway.envoyproxy.io/v1alpha1"
assert_proxy_lb_ip = _proxy.spec.provider.kubernetes.envoyService.loadBalancerIP == "34.63.203.181"
assert_proxy_provider_type = _proxy.spec.provider.type == "Kubernetes"

# The EnvoyProxy sets ONLY loadBalancerIP under envoyService. Setting
# `type` or `externalTrafficPolicy` here would re-state EG's own
# defaults (LoadBalancer / Local) as forge-owned fields, and any later
# drift between the two would silently rewrite a live prod Service.
assert_proxy_svc_keys = sorted([k for k in _proxy.spec.provider.kubernetes.envoyService]) == ["loadBalancerIP"]

# ── Unchanged behaviour ────────────────────────────────────────────

# NamedAddress is a different mechanism (GKE cloud controller, global
# reservation by name) and EG's translator ignores it, so it stays in
# spec.addresses and mints no EnvoyProxy.
assert_named_keeps_addresses = _named_gw.spec.addresses[0].type == "NamedAddress"
assert_named_addr_value = _named_gw.spec.addresses[0].value == "my-reserved-ip"
assert_named_no_params = not _named_gw.spec.get("infrastructure")

# Unpinned Gateway is untouched in both directions.
assert_unpinned_no_addresses = not _internal_gw.spec.get("addresses")
assert_unpinned_no_params = not _internal_gw.spec.get("infrastructure")

# ── Machine-facing contract ────────────────────────────────────────

# The JSON contract keeps reporting what the user DECLARED, so audit /
# doctor tooling can still see the pinned address without knowing which
# of the two mechanisms carries it to the cluster.
contract = forge.render(_bundle)
_public_contract = [g for g in contract.gateways if g.name == "public"][0]
assert_contract_addr_type = _public_contract.addresses[0].type == "IPAddress"
assert_contract_addr_value = _public_contract.addresses[0].value == "34.63.203.181"
