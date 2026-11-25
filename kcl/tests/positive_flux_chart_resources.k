"""
Positive test — `flux_chart(resources = ...)` sizes each controller, merged
per field over forge's defaults (forge #1826: 1.25 CPU requested for ~20m used
on a hub). Unnamed controllers and unnamed fields keep forge's sizing, so the
default render is unchanged.
"""

import forge
import yaml

_pod_for = lambda flux: any, name: str -> any {
    _fi = [o for o in flux.manifests if o.kind == "FluxInstance"][0]
    [p.spec.template.spec for p in [yaml.decode(q.patch) for q in _fi.spec.kustomize.patches if q.target.kind == "Deployment" and q.target?.name == "^${name}$"] if typeof(p) == "dict"][0]
}
_res = lambda flux: any, name: str -> any { _pod_for(flux, name).containers[0].resources }

_default = forge.flux_chart()
_sized = forge.flux_chart(resources = {
    kustomize = {requests = {cpu = "50m", memory = "128Mi"}}
    source = {requests = {cpu = "20m"}, limits = {memory = "512Mi"}}
})
_shard = forge.flux_chart(
    connected_shard = True
    egress = {kustomize = [{}], source = [{}], kustomize_connected = [{}], operator = [{}]}
    resources = {kustomize_connected = {requests = {cpu = "10m"}}}
)

_kc = _res(_sized, "kustomize-controller")
_sc = _res(_sized, "source-controller")
assert_default_unchanged = _res(_default, "kustomize-controller") == {requests = {cpu = "500m", memory = "512Mi"}, limits = {cpu = "2", memory = "2Gi"}} and _res(_default, "source-controller") == {requests = {cpu = "250m", memory = "256Mi"}, limits = {cpu = "1", memory = "1Gi"}}
assert_kustomize_requests_set = _kc.requests == {cpu = "50m", memory = "128Mi"}
assert_kustomize_limits_kept = _kc.limits == {cpu = "2", memory = "2Gi"}
assert_source_merged_per_field = _sc.requests == {cpu = "20m", memory = "256Mi"} and _sc.limits == {cpu = "1", memory = "512Mi"}
assert_shard_sized_alone = _res(_shard, "kustomize-controller-connected").requests == {cpu = "10m", memory = "512Mi"} and _res(_shard, "kustomize-controller").requests == {cpu = "500m", memory = "512Mi"}
