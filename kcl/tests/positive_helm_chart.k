"""
Positive test — HelmChart platform deps render through the JSON
contract (top-level `helm_charts` bucket).

THE MODEL: helm is a RENDERER, not an installer. A HelmChart is a
renderable with a NAME the EXISTING `--target` axis selects. forge reads
this `output.helm_charts` list and runs `helm template <chart|oci>
--version <v> --values <vals> -n <ns> --skip-crds` Go-side, stamping
`app.kubernetes.io/name = name` on every rendered manifest so
`forge env deploy <env> --target <name>` applies that platform dep.

cert-manager + Envoy Gateway are the two motivating deps: one repo chart,
one OCI chart — both rendered here. HelmChart NEVER appears in
output.manifests (its expansion is a Go-side helm shell-out, not a KCL
render).
"""

import forge
import forge.workloads as fw

_target = forge.ClusterTarget {
    cluster = "prod-cluster"
    namespace = "platform"
    platform = "amd64"
}

_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _target
    workloads = [
        fw.Workload {
            name = "api"
            image = "ghcr.io/reliant-labs/api"
            ports = [fw.Port {name = "http", port = 8080}]
            runtime = forge.OnCluster {target = _target}
        }
    ]
    helm_charts = [
        # cert-manager — a REPO chart. forge supplies cert-manager's CRDs
        # at the matching chart version FIRST (crds = "cert-manager").
        forge.HelmChart {
            name = "cert-manager"
            chart = "cert-manager"
            repo = "https://charts.jetstack.io"
            version = "v1.20.1"
            namespace = "cert-manager"
            crds = "cert-manager"
            values = {
                installCRDs = False
                replicaCount = 1
            }
        }
        # Envoy Gateway — an OCI chart. forge supplies the pinned standard
        # Gateway API CRDs FIRST (crds = "gateway-api"); the chart's own
        # (older/experimental) CRDs are excluded via --skip-crds.
        forge.HelmChart {
            name = "envoy-gateway"
            oci = "oci://docker.io/envoyproxy/gateway-helm"
            version = "v1.7.2"
            namespace = "envoy-gateway-system"
            crds = "gateway-api"
        }
    ]
}

# ── JSON contract (top-level `output.helm_charts` bucket) ──────────

output = forge.render(_bundle)
assert_hc_bucket_present = "helm_charts" in output
assert_hc_count = len(output.helm_charts) == 2

# cert-manager — repo chart, cert-manager CRD bundle, values passed verbatim.
assert_hc_cm_name = output.helm_charts[0].name == "cert-manager"
assert_hc_cm_chart = output.helm_charts[0].chart == "cert-manager"
assert_hc_cm_repo = output.helm_charts[0].repo == "https://charts.jetstack.io"
assert_hc_cm_oci_null = output.helm_charts[0].oci == None
assert_hc_cm_version = output.helm_charts[0].version == "v1.20.1"
assert_hc_cm_namespace = output.helm_charts[0].namespace == "cert-manager"
assert_hc_cm_crds = output.helm_charts[0].crds == "cert-manager"
assert_hc_cm_values = output.helm_charts[0].values.replicaCount == 1

# Envoy Gateway — OCI chart, gateway-api CRD bundle, default (empty) values.
assert_hc_eg_name = output.helm_charts[1].name == "envoy-gateway"
assert_hc_eg_chart_null = output.helm_charts[1].chart == None
assert_hc_eg_repo_null = output.helm_charts[1].repo == None
assert_hc_eg_oci = output.helm_charts[1].oci == "oci://docker.io/envoyproxy/gateway-helm"
assert_hc_eg_crds = output.helm_charts[1].crds == "gateway-api"
assert_hc_eg_values_empty = len(output.helm_charts[1].values) == 0

# ── HelmChart NEVER renders into the k8s manifest stream ───────────
# Its expansion is a Go-side `helm template` shell-out; KCL emits only
# the declaration. No chart manifest leaks into output.manifests.
manifests = forge.render(_bundle).manifests
_helmish = [m for m in manifests if "envoyproxy" in str(m) or m.kind == "ClusterIssuer"]
assert_no_helm_manifests_in_render = len(_helmish) == 0
