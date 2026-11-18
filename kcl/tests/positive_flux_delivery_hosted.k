"""
Positive test — `forge.flux_delivery_resourceset()` in the control-plane hub's
shape: one ResourceSet per ledger binding, its input inline, running as the
delivery ServiceAccount, rendering FLUX_DELIVERY_TEMPLATE and nothing else.

WHAT THIS PINS:

  * THE TEMPLATE IS THE ONE TEMPLATE. `resourcesTemplate` is the exported
    constant, the same string Go reads as forge/pkg/fluxdelivery.Template —
    there is no argument that could substitute another (see
    closedschema_flux_delivery_template_override.k).
  * THE RESOURCESET NAMES ITS SERVICEACCOUNT. One that names none runs as
    flux-operator's --default-service-account, or in the operator's own
    namespace as the operator itself.
  * NO `resources`, NO `steps`, NO `inputsFrom` on the hub: the writer owns
    every input, and the hub's admission policy refuses the rest.
  * THE RBAC IS THE TWO FLUX KINDS AND NOTHING ELSE — in particular no Secret
    verb, because a Secret `create` in a hub namespace mints a token for any
    ServiceAccount there.
"""

import forge

_input = {
    schema = "forge.dev/flux-delivery/v1"
    org = "acme"
    env = "env-prod"
    binding = ""
}

_rs = forge.flux_delivery_resourceset("delivery.env-prod", "flux-acme", inputs = [_input])
_rbac = forge.flux_delivery_rbac("flux-acme")
_role = [o for o in _rbac if o.kind == "Role"][0]

assert_kind = _rs.apiVersion == "fluxcd.controlplane.io/v1" and _rs.kind == "ResourceSet"
assert_name = _rs.metadata.name == "delivery.env-prod" and _rs.metadata.namespace == "flux-acme"
assert_service_account = _rs.spec.serviceAccountName == forge.FLUX_DELIVERY_SA and forge.FLUX_DELIVERY_SA == "flux-delivery"
assert_template = _rs.spec.resourcesTemplate == forge.FLUX_DELIVERY_TEMPLATE
assert_template_versioned = "forge.dev/flux-delivery/v1" in forge.FLUX_DELIVERY_TEMPLATE
assert_inline_inputs = _rs.spec.inputs == [_input]
assert_spec_keys = sorted([k for k in _rs.spec]) == ["inputs", "resourcesTemplate", "serviceAccountName"]
assert_rbac_kinds = [o.kind for o in _rbac] == ["ServiceAccount", "Role", "RoleBinding"]
assert_rbac_namespace = all o in _rbac { o.metadata.namespace == "flux-acme" and o.metadata.name == "flux-delivery" }
assert_rbac_flux_kinds_only = sorted([r.resources[0] for r in _role.rules]) == ["kustomizations", "ocirepositories"] and all r in _role.rules { len(r.resources) == 1 }
assert_rbac_no_secrets = all r in _role.rules { "secrets" not in r.resources and "" not in r.apiGroups }
assert_constants = forge.FLUX_DELIVERY_PRUNE_ANNOTATION == "fluxcd.controlplane.io/prune" and forge.FLUX_DELIVERY_PRUNE_DISABLED == "disabled" and forge.FLUX_DELIVERY_RETIRE_ORPHAN == "orphan" and forge.FLUX_DELIVERY_RETIRE_TEARDOWN == "teardown"
