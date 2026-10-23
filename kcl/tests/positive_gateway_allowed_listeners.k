"""
Positive test — `Gateway.allowed_listeners` renders to
spec.allowedListeners, and is OMITTED when unset.

WHY THIS FIELD EXISTS. A ListenerSet (Gateway API v1.5+, standard
channel) declares listeners in its own object that attach to a Gateway,
which is how a controller adds a per-hostname TLS listener without
read-modify-writing the shared Gateway. Gateway API DENIES every
ListenerSet by default, so without spec.allowedListeners the attachment
fails — verified on Envoy Gateway v1.9.2 / Gateway API v1.6.2:

    Accepted=False  reason=NotAllowed
    "ListenerSet attachment from namespace <ns> not allowed by Gateway"

THE OMISSION CASE IS LOAD-BEARING, not cosmetic. Rendering an empty
allowedListeners block on every Gateway would state Gateway API's own
default as explicit config and churn the spec of every existing Gateway
in every project, for no behavioural change.
"""

import forge
import forge.workloads as fw

_target = forge.ClusterTarget {
    cluster = "k3d-example"
    namespace = "example-dev"
}

_workload = fw.Workload {
    name = "api"
    image = "localhost:5050/example"
    ports = [fw.Port {name = "http", port = 8080}]
    runtime = forge.OnCluster {target = _target}
}

_listener = forge.GatewayListener {
    name = "http"
    port = 18080
    protocol = "HTTP"
}

# ── A Gateway that admits ListenerSets from its OWN namespace ──────
_same = forge.Bundle {
    project = "fixture"
    cluster_target = _target
    workloads = [_workload]
    gateways = [
        forge.Gateway {
            name = "public"
            host = "api.example.com"
            listeners = [_listener]
            allowed_listeners = forge.GatewayAllowedListeners {
                from_namespaces = "Same"
            }
        }
    ]
}

# ── A Gateway that admits them from SELECTED namespaces ────────────
_selector = forge.Bundle {
    project = "fixture"
    cluster_target = _target
    workloads = [_workload]
    gateways = [
        forge.Gateway {
            name = "public"
            host = "api.example.com"
            listeners = [_listener]
            allowed_listeners = forge.GatewayAllowedListeners {
                from_namespaces = "Selector"
                selector = {"kubernetes.io/metadata.name" = "edge-certs"}
            }
        }
    ]
}

# ── A Gateway that says nothing (the overwhelmingly common case) ───
_unset = forge.Bundle {
    project = "fixture"
    cluster_target = _target
    workloads = [_workload]
    gateways = [
        forge.Gateway {
            name = "public"
            host = "api.example.com"
            listeners = [_listener]
        }
    ]
}

_gw = lambda b: any -> any {
    [m for m in forge.render(b).manifests if m.kind == "Gateway"][0]
}

# ── Invariants ─────────────────────────────────────────────────────

# "Same" renders the namespaces.from discriminator and NO selector.
assert_same_from = _gw(_same).spec.allowedListeners.namespaces.from == "Same"
assert_same_no_selector = "selector" not in _gw(_same).spec.allowedListeners.namespaces

# "Selector" carries matchLabels through.
assert_sel_from = _gw(_selector).spec.allowedListeners.namespaces.from == "Selector"
assert_sel_labels = _gw(_selector).spec.allowedListeners.namespaces.selector.matchLabels == {"kubernetes.io/metadata.name" = "edge-certs"}

# UNSET OMITS THE KEY ENTIRELY — see the docstring.
assert_unset_omitted = "allowedListeners" not in _gw(_unset).spec

# The field changes nothing else about the Gateway.
assert_same_class = _gw(_same).spec.gatewayClassName == "eg"
assert_same_listener_port = _gw(_same).spec.listeners[0].port == 18080
