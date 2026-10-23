"""
Positive test — a GCPSecretManager bootstrap source resolves to the Go renderer's
Bootstrap record and a `bootstrap` block, with the identities derived from the
store's name (the names control-plane's provisioning created) and overridable:

  * the seal key and the Postgres password are DELIVERED (no prerequisite is
    declared), each with the secret id the BootstrapSecret names;
  * the seal key is minted at deploy (BootstrapSecret.generate), the Postgres
    password is not unless asked, and the recovery escrow never is;
  * the reader and the escrow writer are two different Google identities.
"""

# kcl-args: env=prod

import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_us-central1_prod", namespace = "acme-prod", platform = "amd64", connected_cluster = "hub"}

_bundle = forge.Bundle {
    project = "acme"
    cluster_target = _hub
    openbao = forge.OpenBao {
        namespace = "acme-prod"
        replicas = 2
        storage = forge.OpenBaoPostgres {
            cloudsql_instance = "acme:us-central1:pg"
            gcp_service_account = "openbao@acme.iam.gserviceaccount.com"
            password = forge.BootstrapSecret {name = "openbao-pg-password-prod"}
        }
    }
    external_secrets_operators = [forge.ExternalSecretsOperator {cluster = _hub, namespace = "eso"}]
    bootstrap_source = forge.GCPSecretManager {project = "acme-prod"}
}

output = forge.render(_bundle)

_bao = output.openbao
_rec = [m for m in output.manifests if m.kind == "OpenBao"][0]

assert_no_prereqs = len(output.required_secrets) == 0
assert_spec_project = _rec.spec.bootstrap.project == "acme-prod"
assert_default_reader_account = _rec.spec.bootstrap.readerServiceAccount == "openbao-bootstrap-reader"
assert_default_reader_gsa = _rec.spec.bootstrap.readerGSA == "openbao-bootstrap-reader@acme-prod.iam.gserviceaccount.com"
assert_default_escrow_gsa = _rec.spec.bootstrap.escrowGSA == "openbao-escrow-writer@acme-prod.iam.gserviceaccount.com"
assert_two_identities = _rec.spec.bootstrap.readerGSA != _rec.spec.bootstrap.escrowGSA
assert_eso_namespace = _rec.spec.bootstrap.esoNamespace == "eso"
assert_secret_ids = _rec.spec.bootstrap.sealSecretID == "openbao-unseal-prod" and _rec.spec.bootstrap.passwordSecretID == "openbao-pg-password-prod" and _rec.spec.bootstrap.escrowSecretID == "openbao-recovery-prod"
assert_block = _bao.bootstrap.escrow_service_account == "openbao-converge" and _bao.bootstrap.namespace == "acme-prod" and _bao.bootstrap.password_secret == "openbao-pg-password-prod"
assert_names = _bao.bootstrap_names == ["openbao-pg-password-prod", "openbao-recovery-prod", "openbao-unseal-prod"]
assert_seal_generated = _bao.bootstrap_generate["openbao-unseal-prod"].kind == "random" and _bao.bootstrap_generate["openbao-unseal-prod"].bytes == 32 and _bao.bootstrap_generate["openbao-unseal-prod"].encoding == "hex"
assert_only_the_seal_is_minted = sorted([k for k, _v in _bao.bootstrap_generate]) == ["openbao-unseal-prod"]
assert_no_escrow_warning = len([w for w in _bao.warnings if "inside the cluster" in w]) == 0
