"""
Positive test — an HA OpenBao spreads over nodes as a hard requirement by
default and renders no node-spread warning. Overriding to a preference needs a
reason, is carried into the spec, and warns on every render (only with more
than one replica: a single replica has nothing to spread).
"""

import forge

_t = forge.ClusterTarget {cluster = "gke_acme_us-central1_prod", namespace = "acme-prod", platform = "amd64"}

_default = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    cluster_target = _t
    openbao = forge.OpenBao {}
})
_relaxed = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    cluster_target = _t
    openbao = forge.OpenBao {
        ha = forge.OpenBaoHA {node_spread = "preferred", node_spread_reason = "two-node staging pool"}
    }
})
_single = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    cluster_target = _t
    openbao = forge.OpenBao {
        replicas = 1
        ha = forge.OpenBaoHA {node_spread = "preferred", node_spread_reason = "one replica"}
    }
})

output = _relaxed

_warns = lambda r: any -> [str] {
    [w for w in r.openbao.warnings if "only as a preference" in w]
}

_spec = lambda r: any -> any {
    [m for m in r.manifests if m.kind == "OpenBao"][0].spec
}

assert_default_is_required = _spec(_default).ha.nodeSpread == "required"
assert_default_does_not_warn = len(_warns(_default)) == 0
assert_relaxed_carries_reason = _spec(_relaxed).ha.nodeSpread == "preferred" and _spec(_relaxed).ha.nodeSpreadReason == "two-node staging pool"
assert_relaxed_warns = len(_warns(_relaxed)) == 1 and "two-node staging pool" in _warns(_relaxed)[0]
assert_single_replica_does_not_warn = len(_warns(_single)) == 0
