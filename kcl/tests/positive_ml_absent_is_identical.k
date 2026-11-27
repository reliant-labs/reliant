"""
Positive test — the ML stack is additive.

An env that declares no stack renders byte-identically to one that declares
`ml_stacks = []` (and the render-contract goldens, which predate MLStack and
are unchanged, prove the same for every runtime). A stack of adoptions with no
port lowers to nothing in the cluster: the render differs only by the
`ml_stacks` projection, with observability on or off.
"""

import json
import forge
import forge.workloads as fw

_gpu = forge.ClusterTarget {cluster = "gke_acme_us-central1_gpu", namespace = "forge-ml", platform = "amd64"}

_base = lambda stacks: any, obs: bool -> any {
    forge.Bundle {
        project = "acme"
        env = "prod"
        if obs:
            observability = forge.Observability {destination = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}}
        workloads = [fw.Workload {name = "api", image = "registry.example.com/acme/api:1", ports = [fw.Port {name = "http", port = 8080}], runtime = forge.OnCluster {target = _gpu}}]
        if stacks != None:
            ml_stacks = stacks
    }
}

_adopt_only = [forge.MLStack {
    cluster = _gpu
    observed = ["kueue", "dcgm-exporter"]
    accelerators = [forge.Accelerators {vendor = "nvidia", metrics = forge.Adopt {kind = "dcgm-exporter", namespace = "gpu-operator"}}]
    queue = forge.Adopt {kind = "kueue", namespace = "kueue-system"}
}]

_without = forge.render(_base(None, True))
_empty = forge.render(_base([], True))
_adopted = forge.render(_base(_adopt_only, True))
_without_dark = forge.render(_base(None, False))
_adopted_dark = forge.render(_base(_adopt_only, False))

_drop_ml = lambda out: any -> any {
    {k: v for k, v in out if k != "ml_stacks"}
}

assert_empty_is_identical = json.encode(_without) == json.encode(_empty)
assert_no_ml_key = "ml_stacks" not in _without and "bundled_charts" not in _without
assert_adopt_only_lowers_to_nothing = json.encode(_drop_ml(_adopted)) == json.encode(_without)
assert_adopt_only_lowers_to_nothing_dark = json.encode(_drop_ml(_adopted_dark)) == json.encode(_without_dark)
assert_adopt_only_is_projected = [c.kind for c in _adopted.ml_stacks[0].components] == ["dcgm-exporter", "kueue"]
