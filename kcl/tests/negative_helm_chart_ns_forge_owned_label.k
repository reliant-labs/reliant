"""
Negative test — HelmChart namespace metadata / prune is refused when it cannot
mean what it says (forge_owned_label).
"""

# expect: forge and the apiserver own those

import forge

_chart = forge.HelmChart {
    name = "bad"
    oci = "oci://example.com/bad"
    version = "1.0.0"
    namespace = "bad"
    namespace_labels = {"forge.dev/chart-owner" = "x"}
}
