"""
Positive test — `identity = X` is sugar for `identities = [X]`: the two
spellings render byte-identically (the whole output, manifests, env and shape
included), and both record the one identity as `identity`, so a control plane
that reads only that key converges it either way. Covered for a cluster, a
hosted and a host-run identity, a renamed one, and a sharer.
"""

import json
import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

_cluster = lambda idn: any, list: bool -> any {
    forge.render(forge.Bundle {
        project = "acme"
        env = "prod"
        control_plane = forge.ControlPlane {}
        cluster_target = _prod
        if list:
            identities = [idn]
        else:
            identity = idn
        workloads = [
            fw.Workload {name = "api", image = "ghcr.io/acme/api", ports = [fw.Port {name = "http", port = 8080, expose = True}], runtime = forge.OnCluster {target = _prod}}
            fw.Workload {name = "worker", kind = "worker", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}
            fw.Workload {name = "reporter", image = "ghcr.io/acme/reporter", env = {X = forge.IdentityRef {output = "issuer"}}, runtime = forge.OnCluster {target = _prod}}
        ]
    })
}

_hosted = lambda idn: any, list: bool -> any {
    forge.render(forge.Bundle {
        project = "acme"
        env = "preview-1"
        control_plane = forge.ControlPlane {}
        secret_provider = forge.HostedSecrets {}
        if list:
            identities = [idn]
        else:
            identity = idn
        workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnHosted {}}]
    })
}

_host = lambda idn: any, list: bool -> any {
    forge.render(forge.Bundle {
        project = "acme"
        env = "dev"
        lifecycle = "local"
        infra = [
            forge.HostInfra {name = "pg", engine = "postgres", port = 5432}
            forge.HostInfra {name = "idp", engine = "zitadel", port = 8098, idp_database_port = 5432}
        ]
        if list:
            identities = [idn]
        else:
            identity = idn
        workloads = [fw.Workload {name = "api", kind = "service", runtime = forge.OnHost {runner = "binary"}, build = forge.GoBuild {cmd = "./cmd/acme", output_name = "acme"}, args = ["server"]}]
    })
}

_same = lambda r: any, idn: any -> bool {
    json.encode(r(idn, False)) == json.encode(r(idn, True))
}

_roles = forge.ManagedIdentity {roles = [forge.IdentityRole {key = "admin"}, forge.IdentityRole {key = "member"}]}

assert_cluster_identical = _same(_cluster, _roles)
assert_cluster_renamed_identical = _same(_cluster, _roles | {name = "ops"})
assert_hosted_identical = _same(_hosted, forge.ManagedIdentity {share_with = "staging"})
assert_host_identical = _same(_host, forge.ManagedIdentity {runtime = forge.IdentityOnHost {infra = "idp"}, seed_users = [forge.IdentitySeedUser {email = "dev@acme.test"}]})
assert_list_records_identity = _cluster(_roles, True).identity.name == "identity" and "identities" not in _cluster(_roles, True)
