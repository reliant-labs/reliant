"""
Negative test — a HelmChart patch value must be a map of fields.
"""

# expect: patches values must be maps

import forge

_c = forge.HelmChart {
    name = "x"
    oci = "oci://example.com/x"
    version = "1.0.0"
    namespace = "x"
    patches = {"Deployment/x" = "gvisor"}
}
