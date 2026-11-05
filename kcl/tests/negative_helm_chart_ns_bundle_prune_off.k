"""
Negative test — HelmChart namespace metadata / prune is refused when it cannot
mean what it says (bundle_prune_off).
"""

# expect: has no effect on a delivery 'bundle' chart

import forge

_chart = forge.HelmChart {
    name = "bad"
    oci = "oci://example.com/bad"
    version = "1.0.0"
    namespace = "bad"
    delivery = "bundle"
    prune = False
}
