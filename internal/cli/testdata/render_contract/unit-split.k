"""
render contract — an env mid-move onto the hub, one delivery unit at a time
(`Bundle.hub_converges`): `reliant` is converged by the hub, `infra` and the
default unit are still applied by forge, and a Firebase frontend forge ships
belongs to no unit. Pins that the units, the split and the per-object unit
labels reach the decoder, and that an undeclared env projects no
`hub_converges` key at all (see host.json).
"""
# kcl-args: env="prod"

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {
    cluster = "gke_acme_prod"
    namespace = "acme-prod"
    connected_cluster = "acme-prod"
    platform = "amd64"
}
_image = "ghcr.io/acme/acme"
_build = forge.GoBuild {cmd = "./cmd/acme", output_name = "acme"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {endpoint = "https://cp.acme.example"}
    cluster_target = _prod
    delivery_units = [
        forge.DeliveryUnit {name = "reliant", after = []}
        forge.DeliveryUnit {name = "infra"}
    ]
    hub_converges = ["reliant"]
    workloads = [
        fw.Workload {
            name = "api"
            image = _image
            build = _build
            args = ["api"]
            ports = [fw.Port {name = "http", port = 8080, expose = True}]
            delivery_unit = "reliant"
            runtime = forge.OnCluster {target = _prod}
        }
        fw.Workload {
            name = "nats"
            image = "ghcr.io/nats-io/nats:2.10"
            ports = [fw.Port {name = "client", port = 4222}]
            delivery_unit = "infra"
            runtime = forge.OnCluster {target = _prod}
        }
        fw.Workload {
            name = "admin"
            image = _image
            build = _build
            args = ["admin"]
            ports = [fw.Port {name = "http", port = 8081, expose = True}]
            runtime = forge.OnCluster {target = _prod}
        }
    ]
    frontends = [
        forge.Frontend {
            name = "web"
            type = "vite"
            path = "frontends/web"
            runtime = forge.OnFirebase {project = "acme-prod", site = "acme"}
        }
    ]
})
