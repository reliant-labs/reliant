"""
Positive test — retired auth mounts reach the OpenBao record, reason and force
included, and a forced one warns on every render. An unforced list renders no
retire warning, and an OpenBao that retires nothing carries no field at all.
"""

import forge

_t = forge.ClusterTarget {cluster = "gke_acme_us-central1_prod", namespace = "acme-prod", platform = "amd64"}

_none = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    cluster_target = _t
    openbao = forge.OpenBao {}
})
_retired = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    cluster_target = _t
    openbao = forge.OpenBao {
        retired_auth_mounts = [
            forge.OpenBaoRetiredAuthMount {name = "kubernetes", reason = "no login since 2026-10-25 11:41Z (audit log)"}
            forge.OpenBaoRetiredAuthMount {name = "approle", reason = "no login since 2026-10-25 12:27Z (audit log)", force = True}
        ]
    }
})

output = _retired

_spec = lambda r: any -> any {
    [m for m in r.manifests if m.kind == "OpenBao"][0].spec
}
_warns = lambda r: any -> [str] {
    [w for w in r.openbao.warnings if "retired auth mount" in w]
}

assert_none_declared_has_no_field = "retiredAuthMounts" not in _spec(_none)
assert_two_mounts_reach_the_record = [m.name for m in _spec(_retired).retiredAuthMounts] == ["kubernetes", "approle"]
assert_reason_travels = "audit log" in _spec(_retired).retiredAuthMounts[0].reason
assert_force_is_omitted_unless_set = "force" not in _spec(_retired).retiredAuthMounts[0] and _spec(_retired).retiredAuthMounts[1].force == True
assert_only_the_forced_one_warns = len(_warns(_retired)) == 1 and "approle" in _warns(_retired)[0]
assert_no_warning_without_force = len(_warns(_none)) == 0
