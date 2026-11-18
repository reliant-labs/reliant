"""
Negative test — a selector set alongside `from_namespaces = "Same"` is
refused.

Gateway API reads `selector` only for the Selector form, so this
combination is a silent no-op: the author believes they scoped
attachment to labelled namespaces, and the rendered Gateway admits the
Gateway's own namespace instead. The two readings differ in who may
terminate TLS on this load balancer, so it is refused rather than
dropped.

# expect: selector is only used when from_namespaces == 'Selector'
"""

import forge
import forge.workloads as fw

_target = forge.ClusterTarget {
    cluster = "k3d-example"
    namespace = "example-dev"
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
        forge.Gateway {
            name = "public"
            host = "api.example.com"
            listeners = [
                forge.GatewayListener {
                    name = "http"
                    port = 18080
                    protocol = "HTTP"
                }
            ]
            allowed_listeners = forge.GatewayAllowedListeners {
                from_namespaces = "Same"
                selector = {"kubernetes.io/metadata.name" = "edge-certs"}
            }
        }
    ]
}

manifests = forge.render(_bundle).manifests
