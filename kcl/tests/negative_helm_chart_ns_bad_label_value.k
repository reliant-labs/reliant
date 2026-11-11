"""
Negative test — HelmChart namespace metadata / prune is refused when it cannot
mean what it says (bad_label_value).
"""

# expect: not a valid Kubernetes label value

import forge

_chart = forge.HelmChart {
    name = "bad"
    oci = "oci://example.com/bad"
    version = "1.0.0"
    namespace = "bad"
    namespace_labels = {"team" = "has space"}
}
