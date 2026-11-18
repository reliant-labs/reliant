"""full_stack — the single authoritative cluster-mode Bundle assembly.

TRIMMED COPY of control-plane's real deploy/kcl/lib/stack.k. The shape that
matters to the migration is kept exactly: every cluster env calls this ONE
lambda, and the per-workload bindings live HERE rather than in the env's own
main.k. A reader that looks only at deploy/kcl/<env>/main.k therefore finds no
bindings at all for these envs.
"""

import forge
import forge.workloads as fw
import ..workloads as wl

schema StackParams:
    cluster: forge.ClusterTarget
    proxy_cluster?: forge.ClusterTarget

full_stack = lambda p: StackParams -> forge.Bundle {
    _on_primary = forge.OnCluster {target = p.cluster}

    # The proxy's cluster: the daemon cluster in a daemon-split env.
    _on_proxy = forge.OnCluster {target = p.proxy_cluster} if p.proxy_cluster else _on_primary

    forge.Bundle {
        project = "control-plane"
        cluster_target = p.cluster
        workloads = [
            wl.admin_server | {
                runtime = _on_primary
            }
            wl.admin_api | {
                runtime = _on_primary
            }
            wl.control_plane_workers | {
                runtime = _on_primary
            }
            wl.workspace_proxy | {
                runtime = _on_proxy
            }
            wl.reliant_api_server | {
                runtime = _on_primary
            }
            wl.reliant_temporal_worker | {
                runtime = _on_primary
            }
            wl.daemon_gateway | {
                runtime = _on_primary
            }
            wl.workspace_controller | {
                runtime = _on_primary
            }
            wl.control_plane_migrate | {
                runtime = _on_primary
            }
        ]
    }
}
