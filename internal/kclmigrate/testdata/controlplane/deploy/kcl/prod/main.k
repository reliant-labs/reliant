"""prod — GKE, everything in-cluster, assembled through stack.full_stack.

TRIMMED COPY of control-plane's real deploy/kcl/prod/main.k. The registry is a
literal bound to `_registry` and referenced indirectly on TWO ClusterTargets,
and this env binds NO workload in its own file — every binding is in
lib/stack.k, reached through the `stack.full_stack` call.
"""

import forge
import forge.workloads as fw
import lib.stack
import ..workloads as wl

_namespace = "control-plane-prod"

# Prod's image registry: prod GAR, in the prod GCP project, beside the prod GKE
# cluster that pulls from it.
_registry = "us-central1-docker.pkg.dev/reliant-labs-475814/reliant-prod"

_prod_k8s = forge.ClusterTarget {
    cluster = "gke_reliant-labs-475814_us-central1_prod"
    namespace = _namespace
    registry = _registry
    image_pull_secrets = ["ghcr-pull"]
    platform = "amd64"
}

_daemon_target = forge.ClusterTarget {
    cluster = "gke_reliant-labs-475814_us-central1_prod-daemon"
    namespace = _namespace
    registry = _registry
    platform = "amd64"
}

_bundle = stack.full_stack(stack.StackParams {
    cluster = _prod_k8s
})

output = forge.render(_bundle)
