"""
Positive test — the bootstrap identities are configurable. A service account name
changes the account the ESO store logs in as; a bare Google service account id
takes the bootstrap project's domain and a full email is used as given; the
Postgres password can be minted too, and Raft storage has no password to deliver.
"""

# kcl-args: env=prod

import forge

_t = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo"}

_pg = forge.Bundle {
    project = "demo"
    cluster_target = _t
    openbao = forge.OpenBao {
        replicas = 1
        storage = forge.OpenBaoPostgres {
            host = "pg.demo.svc"
            password = forge.BootstrapSecret {name = "pg-pw", generate = forge.GeneratedSecret {kind = "password", length = 24}}
        }
    }
    bootstrap_source = forge.GCPSecretManager {
        project = "acme-prod"
        reader_service_account = "bao-reader"
        reader_gsa = "bao-reader-gsa"
        escrow_gsa = "writer@other-project.iam.gserviceaccount.com"
    }
}
_raft = forge.Bundle {
    project = "demo"
    cluster_target = _t
    openbao = forge.OpenBao {replicas = 1}
    bootstrap_source = forge.GCPSecretManager {project = "acme-prod"}
}

_pg_out = forge.render(_pg)
_raft_out = forge.render(_raft)
_pg_rec = [m for m in _pg_out.manifests if m.kind == "OpenBao"][0]
_raft_rec = [m for m in _raft_out.manifests if m.kind == "OpenBao"][0]

output = _pg_out

assert_service_account = _pg_rec.spec.bootstrap.readerServiceAccount == "bao-reader"
assert_bare_id_takes_the_project = _pg_rec.spec.bootstrap.readerGSA == "bao-reader-gsa@acme-prod.iam.gserviceaccount.com"
assert_email_is_kept = _pg_rec.spec.bootstrap.escrowGSA == "writer@other-project.iam.gserviceaccount.com"
assert_password_minted = _pg_out.openbao.bootstrap_generate["pg-pw"].kind == "password" and _pg_out.openbao.bootstrap_generate["pg-pw"].length == 24
assert_raft_has_no_password = "passwordSecretID" not in _raft_rec.spec.bootstrap
assert_raft_names = _raft_out.openbao.bootstrap_names == ["openbao-recovery-prod", "openbao-unseal-prod"]
