"""
Positive test — `delivery-v1` placement (F8 part 2).

forge has no node-pool primitive, so the pool is provisioned outside KCL; this
pins what forge does with it: the controllers AND the operator get the node
selector and the toleration, and a sandbox RuntimeClass only when the consumer
names one. The controllers get it through the FluxInstance's pod patches; the
operator through its chart values and, for `runtimeClassName` (not a chart
value), HelmChart.patches.
"""

import forge
import yaml

_pool = forge.flux_placement_delivery_v1()
_pool_gv = forge.flux_placement_delivery_v1(runtime_class_name = forge.FLUX_RUNTIME_CLASS_GVISOR)
_p = forge.flux_chart(placement = _pool)
_g = forge.flux_chart(placement = _pool_gv)
_pod = lambda c: any, name: str -> any {
    [yaml.decode(p.patch) for m in c.manifests if m.kind == "FluxInstance" for p in m.spec.kustomize.patches if p.target.kind == "Deployment" and p.target?.name == "^${name}$" and not p.patch.startswith("- ")][0].spec.template.spec
}
_kc = _pod(_p, "kustomize-controller")
_sc = _pod(_p, "source-controller")
_toleration = [{key = "reliant.dev/pool", operator = "Equal", value = "delivery-v1", effect = "NoSchedule"}]

# The pool alone: selector + toleration on BOTH controllers and the operator, no sandbox.
assert_selector = _kc.nodeSelector == {"reliant.dev/pool" = "delivery-v1"} and _sc.nodeSelector == {"reliant.dev/pool" = "delivery-v1"}
assert_toleration = _kc.tolerations == _toleration and _sc.tolerations == _toleration
assert_operator_placed = _p.values.nodeSelector == {"reliant.dev/pool" = "delivery-v1"} and _p.values.tolerations == _toleration
assert_pool_no_sandbox = "runtimeClassName" not in _kc and "runtimeClassName" not in _p.patches["Deployment/flux-operator"].spec.template.spec

# With gVisor: every Flux pod and the operator.
assert_gvisor_controllers = _pod(_g, "kustomize-controller").runtimeClassName == "gvisor" and _pod(_g, "source-controller").runtimeClassName == "gvisor"
assert_gvisor_operator = _g.patches["Deployment/flux-operator"].spec.template.spec.runtimeClassName == "gvisor"
assert_gvisor_keeps_pool = _pod(_g, "kustomize-controller").nodeSelector == _kc.nodeSelector

# Overridable: another pool, another label, another RuntimeClass.
_other = forge.flux_chart(placement = forge.flux_placement_delivery_v1(runtime_class_name = "kata-qemu", pool_key = "example.com/role", pool_value = "gitops"))
assert_other_pool = _pod(_other, "kustomize-controller").nodeSelector == {"example.com/role" = "gitops"}
assert_other_toleration = _pod(_other, "source-controller").tolerations[0].key == "example.com/role"
assert_other_runtime = _pod(_other, "source-controller").runtimeClassName == "kata-qemu"

# The operator patch goes through the HelmChart schema and is projected.
_out = forge.render(forge.Bundle {project = "fixture", env = "dev", helm_charts = [_g]})
assert_projected = _out.helm_charts[0].patches["Deployment/flux-operator"].spec.template.spec.runtimeClassName == "gvisor"
