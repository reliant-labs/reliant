"""
Negative test — a HelmChart patch key must be `Kind/name`.
"""

# expect: patches keys must be `Kind/name`

import forge

_c = forge.HelmChart {
    name = "x"
    oci = "oci://example.com/x"
    version = "1.0.0"
    namespace = "x"
    patches = {"kustomize-controller" = {spec.replicas = 2}}
}
