"""
Positive test — HelmChart.cluster re-targets ONE platform dep to a
SECOND cluster, and the JSON contract carries the DENORMALIZED kubectl
context rather than the bare cluster name.

THE GAP THIS CLOSES. A platform dependency used to land, always, in the
env's primary cluster. An operator must be installed where the custom
resources it reconciles actually land, and in a multi-cluster env those
are not the same cluster: control-plane's dev runs its control plane on
`k3d-control-plane` but renders every `postgresql.cnpg.io/v1 Cluster`
into `k3d-cp-daemon`. With the CNPG operator on the primary cluster the
CR apply fails:

    error: resource mapping not found ... no matches for kind "Cluster"
    in version "postgresql.cnpg.io/v1"

WHY THE FIELD IS A REFERENCE, NOT A STRING. forge applies a chart with
`kubectl --context <ctx>`, and the context is the Cluster's DERIVED
`.context` (`k3d-<name>`) — not its `name`. A bare string would let
`cluster = "cp-daemon"` name a context that does not exist. Passing the
forge.Cluster INSTANCE means the projected context is derived from the
same object forge reconciles, so the two cannot disagree. This mirrors
Gateway.cluster / HTTPRoute.cluster exactly (see positive_gateway_*.k).

UNSET MUST STAY BYTE-IDENTICAL. The third chart below declares no
cluster and must project `null`, which is what keeps every existing
declaration (Envoy Gateway, Flux) on the env's primary cluster with the
argv it had before the field existed.
"""

import forge
import forge.workloads as fw

_control_plane = forge.Cluster {
    name = "control-plane"
}

# The secondary cluster. `owner` is the same edge control-plane's dev
# declares; it is irrelevant to chart targeting and present only so the
# fixture is shaped like the real multi-cluster env.
_cp_daemon = forge.Cluster {
    name = "cp-daemon"
    owner = _control_plane
    api_port = 6444
}

_target = forge.ClusterTarget {
    cluster = _control_plane.context
    namespace = "control-plane-dev"
    platform = "arm64"
}

_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _target
    clusters = [_control_plane, _cp_daemon]
    workloads = [
        fw.Workload {
            name = "admin-server"
            image = "registry.localhost:5051/admin-server"
            ports = [fw.Port {name = "http", port = 8080}]
            runtime = forge.OnCluster {target = _target}
        }
    ]
    helm_charts = [
        # CloudNativePG, RE-TARGETED to the cluster whose apiserver must
        # serve `postgresql.cnpg.io/v1 Cluster`. This is the declaration
        # the gap made impossible to write.
        forge.HelmChart {
            name = "cloudnative-pg"
            oci = "oci://ghcr.io/cloudnative-pg/charts/cloudnative-pg"
            version = "0.29.0"
            namespace = "cnpg-system"
            cluster = _cp_daemon
            values = {
                crds.create = True
            }
        }
        # A re-targeted chart with RIDING MANIFESTS. Those are applied by
        # a different call site than the chart's own output, and they must
        # follow the chart to its cluster — a GatewayClass applied to the
        # primary would configure a controller that is not there.
        forge.HelmChart {
            name = "secondary-gateway"
            oci = "oci://docker.io/envoyproxy/gateway-helm"
            version = "v1.7.2"
            namespace = "envoy-gateway-system"
            crds = "gateway-api"
            cluster = _cp_daemon
            manifests = [
                {
                    apiVersion = "gateway.networking.k8s.io/v1"
                    kind = "GatewayClass"
                    metadata = {name = "eg"}
                    spec = {controllerName = "gateway.envoyproxy.io/gatewayclass-controller"}
                }
            ]
        }
        # NO cluster — the env's primary. Every pre-existing declaration
        # is this shape and must keep projecting null.
        forge.HelmChart {
            name = "flux"
            oci = "oci://ghcr.io/fluxcd-community/charts/flux2"
            version = "2.19.1"
            namespace = "flux-system"
        }
    ]
}

output = forge.render(_bundle)

# ── the re-targeted chart carries the DERIVED context, not the name ────
assert_hc_count = len(output.helm_charts) == 3
assert_cnpg_name = output.helm_charts[0].name == "cloudnative-pg"
assert_cnpg_cluster_is_context = output.helm_charts[0].cluster == "k3d-cp-daemon"
# The projection is DERIVED from the referenced Cluster, so it equals that
# Cluster's own `.context`. This is the anti-drift property: there is no
# second spelling of the context to get wrong.
assert_cnpg_cluster_derives = output.helm_charts[0].cluster == _cp_daemon.context
# And it is NOT the bare name — the value forge would pass to
# `kubectl --context` is the context, and `cp-daemon` is not a context.
assert_cnpg_cluster_not_name = output.helm_charts[0].cluster != _cp_daemon.name

# A re-targeted chart keeps every other field intact.
assert_cnpg_namespace = output.helm_charts[0].namespace == "cnpg-system"
assert_cnpg_version = output.helm_charts[0].version == "0.29.0"
assert_cnpg_values = output.helm_charts[0].values.crds.create == True

# ── riding manifests travel with their re-targeted chart ───────────────
assert_sec_cluster = output.helm_charts[1].cluster == "k3d-cp-daemon"
assert_sec_has_manifests = len(output.helm_charts[1].manifests) == 1
assert_sec_manifest_kind = output.helm_charts[1].manifests[0].kind == "GatewayClass"
assert_sec_crds_bundle = output.helm_charts[1].crds == "gateway-api"

# ── UNSET => null => the env's primary cluster ─────────────────────────
# The compatibility guarantee. An existing declaration projects no
# cluster, so the Go side resolves the env context and the argv is
# unchanged.
assert_flux_name = output.helm_charts[2].name == "flux"
assert_flux_cluster_null = output.helm_charts[2].cluster == None

# ── still never renders into the k8s manifest stream ───────────────────
# Re-targeting changes WHERE a chart is applied, not WHETHER it is a
# Go-side helm shell-out. No chart manifest may leak into the KCL render.
manifests = forge.render(_bundle).manifests
_helmish = [m for m in manifests if m.kind == "GatewayClass" or "cnpg" in str(m)]
assert_no_helm_manifests_in_render = len(_helmish) == 0
