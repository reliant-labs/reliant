"""
Negative test — `from_namespaces = "Selector"` with no selector is
refused.

An empty selector does not mean "match everything"; it means the
namespaces list matches NOTHING, so every ListenerSet is silently
denied. The Gateway looks configured to admit them and admits none,
and the only symptom is a NotAllowed condition on an object in another
namespace. Refuse it where the mistake is written.

# expect: selector is required when from_namespaces == 'Selector'
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
                from_namespaces = "Selector"
            }
        }
    ]
}

manifests = forge.render(_bundle).manifests
