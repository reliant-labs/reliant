"""
Positive test — `delivery-v1` placement (F8 part 2).

forge has no node-pool primitive, so the pool is provisioned outside KCL; this
pins what forge does with it: the controllers get the node selector and the
toleration, and a sandbox RuntimeClass only when the consumer names one.
`runtimeClassName` is not a chart value, so it reaches the pods as a patch on
the two Deployments (HelmChart.patches), which fails loudly if one is renamed.
"""

import forge

_pool = forge.flux_placement_delivery_v1()
_pool_gv = forge.flux_placement_delivery_v1(runtime_class_name = forge.FLUX_RUNTIME_CLASS_GVISOR)
_p = forge.flux_chart(placement = _pool)
_g = forge.flux_chart(placement = _pool_gv)
_kc = _p.values.kustomizeController
_sc = _p.values.sourceController

# The pool alone: selector + toleration on BOTH controllers, no sandbox.
assert_selector = _kc.nodeSelector == {"reliant.dev/pool" = "delivery-v1"} and _sc.nodeSelector == {"reliant.dev/pool" = "delivery-v1"}
assert_toleration = _kc.tolerations == [{key = "reliant.dev/pool", operator = "Equal", value = "delivery-v1", effect = "NoSchedule"}]
assert_tolerations_equal = _kc.tolerations == _sc.tolerations
assert_pool_no_sandbox = "patches" not in _p

# With gVisor: patches on exactly the two controller Deployments.
assert_gvisor_patch_keys = sorted([k for k, _v in _g.patches]) == ["Deployment/kustomize-controller", "Deployment/source-controller"]
assert_gvisor_patch_value = _g.patches["Deployment/kustomize-controller"].spec.template.spec.runtimeClassName == "gvisor"
assert_gvisor_patch_source = _g.patches["Deployment/source-controller"].spec.template.spec.runtimeClassName == "gvisor"
assert_gvisor_keeps_pool = _g.values.kustomizeController.nodeSelector == _kc.nodeSelector

# Overridable: another pool, another label, another RuntimeClass.
_other = forge.flux_chart(placement = forge.flux_placement_delivery_v1(runtime_class_name = "kata-qemu", pool_key = "example.com/role", pool_value = "gitops"))
assert_other_pool = _other.values.kustomizeController.nodeSelector == {"example.com/role" = "gitops"}
assert_other_toleration = _other.values.sourceController.tolerations[0].key == "example.com/role"
assert_other_runtime = _other.patches["Deployment/source-controller"].spec.template.spec.runtimeClassName == "kata-qemu"

# The patches go through the HelmChart schema and are projected.
_out = forge.render(forge.Bundle {project = "fixture", env = "dev", helm_charts = [_g]})
assert_projected = _out.helm_charts[0].patches["Deployment/kustomize-controller"].spec.template.spec.runtimeClassName == "gvisor"
