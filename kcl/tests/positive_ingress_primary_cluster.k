"""
Positive test — ingress lands on ONE cluster in a multi-cluster env.

The deploy router replicates any object without `forge.dev/cluster` to every
cluster of the env. A cluster that carries only a forge.Manifests group is a
deploy destination too, so an unattributed Gateway was applied there as well:
a second Gateway on the primary's reserved IP, a second ACME order for every
Certificate, and routes whose backend Service does not exist on that cluster.

Pinned here:

  * An unpinned Gateway, its Certificate and its routes are stamped with the
    PRIMARY target's cluster — the rule forge.Manifests already follows.
  * A pinned Gateway's Certificate carries the Gateway's cluster (it used to
    carry none, even when the Gateway was pinned).
  * A route with no `cluster` inherits its parent Gateway's.
  * A route's own `cluster` still wins over its Gateway's.
"""

import forge
import forge.workloads as fw

_primary = forge.ClusterTarget {
    cluster = "gke_example_us-central1_prod"
    namespace = "example-prod"
}

_edge = forge.Cluster { name = "edge" }
_other = forge.Cluster { name = "other" }

_tls = forge.GatewayTLS {
    cert_issuer = "letsencrypt-prod"
    secret_name = "public-tls"
}

_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _primary
    workloads = [
        fw.Workload {
            name = "api"
            image = "localhost:5050/example"
            ports = [fw.Port {name = "http", port = 8080}]
            runtime = forge.OnCluster {target = _primary}
        }
    ]
    gateways = [
        forge.Gateway {
            name = "public"
            host = "api.example.com"
            listeners = [forge.GatewayListener {name = "https", port = 443, protocol = "HTTPS"}]
            tls = _tls
        }
        forge.Gateway {
            name = "edge"
            host = "edge.example.com"
            cluster = _edge
            listeners = [forge.GatewayListener {name = "https", port = 443, protocol = "HTTPS"}]
            tls = forge.GatewayTLS {cert_issuer = "letsencrypt-prod", secret_name = "edge-tls"}
        }
    ]
    http_routes = [
        forge.HTTPRoute {name = "api", gateway = "public", listener = "https", service = "api", port = 8080}
        forge.HTTPRoute {name = "edge-api", gateway = "edge", listener = "https", service = "api", port = 8080}
        forge.HTTPRoute {name = "edge-override", gateway = "edge", listener = "https", service = "api", port = 8080, cluster = _other}
    ]
    grpc_routes = [
        forge.GRPCRoute {name = "edge-grpc", gateway = "edge", listener = "https", service = "api", port = 8080}
    ]
    # A second cluster that carries only raw manifests — the shape that made
    # the router treat it as a deploy destination.
    manifests = [
        forge.Manifests {
            cluster = _edge.context
            objects = [{apiVersion = "v1", kind = "Namespace", metadata = {name = "edge-system"}}]
        }
    ]
}

_m = forge.render(_bundle).manifests
_key = "forge.dev/cluster"
_by = lambda kind: str, name: str -> any {
    [m for m in _m if m.kind == kind and m.metadata.name == name][0]
}

# Unpinned ingress lands on the primary.
assert_public_gateway = _by("Gateway", "public").metadata.labels[_key] == "gke_example_us-central1_prod"
assert_public_cert = _by("Certificate", "public-tls").metadata.labels[_key] == "gke_example_us-central1_prod"
assert_public_route = _by("HTTPRoute", "api").metadata.labels[_key] == "gke_example_us-central1_prod"

# A pinned Gateway's Certificate and routes follow the Gateway.
assert_edge_gateway = _by("Gateway", "edge").metadata.labels[_key] == "k3d-edge"
assert_edge_cert = _by("Certificate", "edge-tls").metadata.labels[_key] == "k3d-edge"
assert_edge_http_route = _by("HTTPRoute", "edge-api").metadata.labels[_key] == "k3d-edge"
assert_edge_grpc_route = _by("GRPCRoute", "edge-grpc").metadata.labels[_key] == "k3d-edge"

# A route's own cluster wins.
assert_route_override = _by("HTTPRoute", "edge-override").metadata.labels[_key] == "k3d-other"

# No ingress object is left for the router to replicate.
_unattributed = [m.metadata.name for m in _m if m.kind in ["Gateway", "Certificate", "HTTPRoute", "GRPCRoute"] and _key not in m.metadata.labels]
assert_no_unattributed_ingress = len(_unattributed) == 0
