"""dev — host processes plus a few in-cluster workloads.

TRIMMED COPY of control-plane's real deploy/kcl/dev/main.k. Kept exactly: the
registry declared ONCE as `_registry = "…"` and referred to indirectly
(`registry = _registry`), the runtimes bound to NAMES (`_on_cp`), and the
PIPE-style bindings (`wl.x | _host_only | {…}`) this env writes.
"""

import forge
import forge.workloads as fw
import lib.stack as stack
import ..workloads as wl

_namespace = "control-plane-dev"

# The image registry this env pushes to and pulls from: the local k3d
# registry. DECLARED here and nowhere else — `forge env build dev --push` and
# `forge registry login dev` read it from this line; no flag, env var or
# forge.yaml key names it.
_registry = "localhost:5051"

# The env-wide ClusterTarget shared by every in-cluster (non-host) workload.
_devhost_k8s = forge.ClusterTarget {
    cluster = "k3d-control-plane-v2"
    namespace = _namespace
    registry = _registry
    platform = "arm64"
}

_proxy_cluster = forge.ClusterTarget {
    cluster = "k3d-cp-daemon-v2"
    namespace = _namespace
    registry = _registry
}

_on_cp = forge.OnCluster {target = _devhost_k8s}
_on_cpd = forge.OnCluster {target = _proxy_cluster}

# A control-plane host process. Air by default.
_host = lambda air: str, listen: [int], working_dir: str = "" -> forge.OnHost {
    forge.OnHost {
        runner = "air"
        air_config = air
        listen_ports = listen
    }
}

# What a host process cannot carry.
_host_only = {
    probes = Undefined
    volumes = Undefined
    resources = Undefined
}

_bundle = forge.Bundle {
    project = "control-plane"
    cluster_target = _devhost_k8s
    workloads = [
        wl.admin_server | _host_only | {
            runtime = _host(".air.admin-server.toml", [8090])
        }
        wl.admin_api | _host_only | {
            runtime = _host(".air.admin-api.toml", [8091])
        }
        wl.control_plane_workers | _host_only | {
            runtime = _host(".air.workers.toml", [8092])
        }
        wl.workspace_proxy | {
            runtime = _on_cpd
        }
        wl.reliant_api_server | _host_only | {
            runtime = _host(".air.api.toml", [9090], "../reliant")
        }
        wl.reliant_temporal_worker | _host_only | {
            runtime = _host(".air.worker.toml", [9091], "../reliant")
        }
        wl.daemon_gateway | {
            runtime = _on_cp
        }
        wl.workspace_controller | {
            runtime = _on_cp
        }
        wl.control_plane_migrate | {
            runtime = _on_cp
        }
    ]
}

output = forge.render(_bundle)
