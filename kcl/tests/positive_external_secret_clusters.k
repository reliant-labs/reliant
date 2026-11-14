"""
Positive test — ExternalSecret.clusters names the clusters a Secret must exist
on, and the JSON contract carries each one's kubectl CONTEXT: a forge.Cluster's
derived `.context`, a forge.ClusterTarget's `cluster`.

THE GAP THIS CLOSES (forge bug report d42b621f). The deploy preflight infers a
Secret's clusters from its namespace. control-plane's prod installs cert-manager
on its daemon cluster too, so the hub's DNS-01 `cloudflare-api-token` was
required there, where no ACME issuer reads it.

UNSET MUST STAY BYTE-IDENTICAL. The second Secret declares no clusters and must
project no `clusters` key, which keeps every existing declaration on the
namespace inference.
"""

import forge
import forge.workloads as fw

_hub = forge.Cluster {
    name = "hub"
}

_daemon = forge.Cluster {
    name = "daemon"
    owner = _hub
    api_port = 6444
}

_target = forge.ClusterTarget {
    cluster = _hub.context
    namespace = "app"
}

_remote = forge.ClusterTarget {
    cluster = "gke_example_us-central1_remote"
    namespace = "app"
}

output = forge.render(forge.Bundle {
    project = "fixture"
    cluster_target = _target
    clusters = [_hub, _daemon]
    workloads = [
        fw.Workload {
            name = "api"
            image = "registry.localhost:5051/api"
            ports = [fw.Port {name = "http", port = 8080}]
            runtime = forge.OnCluster {target = _target}
        }
    ]
    required_secrets = [
        forge.ExternalSecret {
            name = "cloudflare-api-token"
            namespace = "cert-manager"
            keys = ["api-token"]
            clusters = [_hub, _remote]
        }
        forge.ExternalSecret {
            name = "app-token"
            namespace = "app"
            keys = ["token"]
        }
    ]
})

assert_count = len(output.required_secrets) == 2
assert_declared_contexts = output.required_secrets[0].clusters == ["k3d-hub", "gke_example_us-central1_remote"]
assert_declared_derives = output.required_secrets[0].clusters[0] == _hub.context
assert_unset_absent = "clusters" not in output.required_secrets[1]
