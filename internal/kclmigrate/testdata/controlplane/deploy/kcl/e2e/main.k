"""e2e — the whole stack in k3d across two clusters, via stack.full_stack.

TRIMMED COPY of control-plane's real deploy/kcl/e2e/main.k: the same
`_registry` literal on TWO ClusterTargets, and a workspace-proxy re-targeted
onto the second cluster through StackParams.
"""

import forge
import forge.workloads as fw
import lib.stack

_namespace = "control-plane-e2e"

# The image registry, DECLARED: the local k3d registry. `forge build e2e
# --push` pushes here; nothing else names it.
_registry = "localhost:5051"

_e2e_k8s = forge.ClusterTarget {
    cluster = "k3d-control-plane-v2"
    namespace = _namespace
    registry = _registry
}

_proxy_namespace = "control-plane-e2e"
_proxy_cluster = forge.ClusterTarget {
    cluster = "k3d-cp-daemon-v2"
    namespace = _proxy_namespace
    registry = _registry
}

_bundle = stack.full_stack(stack.StackParams {
    cluster = _e2e_k8s
    proxy_cluster = _proxy_cluster
})

output = forge.render(_bundle)
