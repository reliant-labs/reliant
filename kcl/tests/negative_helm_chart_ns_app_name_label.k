"""
Negative test — HelmChart.namespace_labels may not set app.kubernetes.io/name.

forge stamps the chart's `name` there on the Namespace it creates. A declared
value would otherwise silently replace it, so the declaration is refused.
"""

# expect: forge and the apiserver own those on the Namespace forge creates

import forge

_chart = forge.HelmChart {
    name = "flux"
    oci = "oci://example.com/flux"
    version = "1.0.0"
    namespace = "flux-system"
    namespace_labels = {"app.kubernetes.io/name" = "flux2"}
}
