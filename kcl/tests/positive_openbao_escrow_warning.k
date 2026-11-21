"""
Positive test — a real env whose bootstrap source is out of band keeps the seal
key and the recovery output inside the cluster they protect, and the render says
so (a warning, never a refusal). A local env, and a Secret Manager source, do not
warn. The out-of-band prerequisites are still declared.
"""

# kcl-args: env=prod

import forge

_t = forge.ClusterTarget {cluster = "gke_acme_us-central1_prod", namespace = "acme-prod", platform = "amd64"}

_default = forge.render(forge.Bundle {
    project = "acme"
    cluster_target = _t
    openbao = forge.OpenBao {replicas = 1}
})
_explicit = forge.render(forge.Bundle {
    project = "acme"
    cluster_target = _t
    openbao = forge.OpenBao {replicas = 1}
    bootstrap_source = forge.OutOfBand {}
})
_gsm = forge.render(forge.Bundle {
    project = "acme"
    cluster_target = _t
    openbao = forge.OpenBao {replicas = 1}
    bootstrap_source = forge.GCPSecretManager {project = "acme-prod"}
})
_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
_local = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    clusters = [forge.Cluster {name = "acme"}]
    cluster_target = _k3d
    openbao = forge.OpenBao {replicas = 1}
})

output = _default

_warns = lambda r: any -> [str] {
    [w for w in r.openbao.warnings if "inside the cluster they protect" in w]
}

assert_default_warns = len(_warns(_default)) == 1 and "bootstrap_source = forge.GCPSecretManager" in _warns(_default)[0]
assert_out_of_band_warns = len(_warns(_explicit)) == 1
assert_gsm_does_not = len(_warns(_gsm)) == 0
assert_local_does_not = len(_warns(_local)) == 0
assert_prerequisites_remain = sorted([s.name for s in _default.required_secrets]) == ["openbao-unseal"]
