"""
Positive test — a Gateway with `addresses` pins itself to a specific
load-balancer address (Gateway API `spec.addresses`). On GKE a
NamedAddress GatewayAddress references a reserved global static IP by its
reservation name, so the Gateway keeps that IP across delete/recreate
(otherwise a recreated Gateway gets a fresh ephemeral IP and breaks DNS).
A Gateway with no addresses renders with NO `spec.addresses` key — so
existing gateways don't churn and the controller assigns ephemerally.

This fixture covers the NamedAddress mechanism only. A LITERAL IP
(`type = "IPAddress"`) deliberately does NOT render into spec.addresses
— Envoy Gateway would turn it into the Service's spec.externalIPs and
GKE admission rejects that. See positive_gateway_lb_ip.k.
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
        # Pinned Gateway — references a GKE reserved static IP by name.
        forge.Gateway {
            name = "public"
            host = "api.example.com"
            listeners = [
                forge.GatewayListener {
                    name = "http"
                    port = 80
                    protocol = "HTTP"
                }
            ]
            addresses = [
                forge.GatewayAddress {
                    type = "NamedAddress"
                    value = "my-reserved-ip"
                }
            ]
        },
        # Unpinned Gateway — no addresses; must render with no
        # spec.addresses key.
        forge.Gateway {
            name = "internal"
            host = "internal.example.com"
            listeners = [
                forge.GatewayListener {
                    name = "http"
                    port = 18081
                    protocol = "HTTP"
                }
            ]
        }
    ]
}

manifests = forge.render(_bundle).manifests

_gateways = [m for m in manifests if m.kind == "Gateway"]

# ── Invariants ─────────────────────────────────────────────────────

# Pinned Gateway carries spec.addresses with the NamedAddress entry.
_public_gw = [g for g in _gateways if g.metadata.name == "public"][0]
assert_pinned_addr_type = _public_gw.spec.addresses[0].type == "NamedAddress"
assert_pinned_addr_value = _public_gw.spec.addresses[0].value == "my-reserved-ip"
assert_pinned_addr_count = len(_public_gw.spec.addresses) == 1

# Unpinned Gateway renders with NO spec.addresses key — emitting
# `addresses: []` would churn existing gateways.
_internal_gw = [g for g in _gateways if g.metadata.name == "internal"][0]
assert_unpinned_no_addresses = not _internal_gw.spec.get("addresses")

# JSON contract surfaces the addresses so audit / doctor tooling can
# distinguish pinned Gateways without re-deriving.
contract = forge.render(_bundle)
_public_contract = [g for g in contract.gateways if g.name == "public"][0]
_internal_contract = [g for g in contract.gateways if g.name == "internal"][0]
assert_contract_addr_type = _public_contract.addresses[0].type == "NamedAddress"
assert_contract_addr_value = _public_contract.addresses[0].value == "my-reserved-ip"
# Unpinned Gateway projects addresses as null (KCL None → JSON null).
assert_contract_no_addresses = _internal_contract.addresses == None
