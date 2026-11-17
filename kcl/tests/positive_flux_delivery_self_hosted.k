"""
Positive test — `forge.flux_delivery_resourceset()` in the self-hosted shape:
the ResourceSet is a reviewed bootstrap object in flux-system that selects its
inputs from ResourceSetInputProviders by label, so a deploy writes only its
provider and re-applying the bootstrap cannot clobber a pointer.

The template is the same string the hub renders; only where the inputs come
from differs.
"""

import forge

_rs = forge.flux_delivery_resourceset("forge-delivery", "flux-system", inputs_from = {
    matchLabels = {"${forge.FLUX_DELIVERY_INPUT_LABEL}" = "true"}
})
_rbac = forge.flux_delivery_rbac("flux-system")

assert_inputs_from = _rs.spec.inputsFrom == [{
    apiVersion = "fluxcd.controlplane.io/v1"
    kind = "ResourceSetInputProvider"
    selector = {matchLabels = {"forge.dev/delivery" = "true"}}
}]
assert_no_inline_inputs = "inputs" not in _rs.spec
assert_same_template = _rs.spec.resourcesTemplate == forge.FLUX_DELIVERY_TEMPLATE
assert_service_account = _rs.spec.serviceAccountName == "flux-delivery"
assert_rbac_namespace = all o in _rbac { o.metadata.namespace == "flux-system" }
assert_binding_subject = [o for o in _rbac if o.kind == "RoleBinding"][0].subjects == [{kind = "ServiceAccount", name = "flux-delivery", namespace = "flux-system"}]
