"""
Negative test — HelmChart namespace metadata / prune is refused when it cannot
mean what it says (bundle_labels).
"""

# expect: no forge-created Namespace

import forge

_chart = forge.HelmChart {
    name = "bad"
    oci = "oci://example.com/bad"
    version = "1.0.0"
    namespace = "bad"
    delivery = "bundle"
    namespace_labels = {a = "b"}
}
