"""
Negative test — HelmChart.repo and HelmChart.oci are mutually exclusive.
A platform dep is sourced from EITHER a chart repo OR an OCI ref, never
both. Declaring both must fail the schema check.
"""

# expect: HelmChart.repo and HelmChart.oci are mutually exclusive

import forge

_chart = forge.HelmChart {
    name = "bad"
    chart = "cert-manager"
    repo = "https://charts.jetstack.io"
    oci = "oci://docker.io/envoyproxy/gateway-helm"  # both set — illegal
    version = "v1.20.1"
    namespace = "cert-manager"
}
