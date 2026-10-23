"""
Positive test — forge.database_read_only_secret declares the credentials of a
ManagedDatabase's forge_ro role: a basic-auth Secret named <db>-forge-ro, its
password generated once into the store and never rotated, and the ESO object it
renders carries the Secret type and the CloudNativePG reload label.
"""

# kcl-args: env=prod

import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_us-central1_prod", namespace = "acme-prod", platform = "amd64", connected_cluster = "hub"}

_s = forge.database_read_only_secret("order-db", "acme-prod", forge.OpenBaoStore {})

_out = forge.render(forge.Bundle {
    project = "fixture"
    cluster_target = _hub
    openbao = forge.OpenBao {}
    external_secrets_operators = [forge.ExternalSecretsOperator {cluster = _hub}]
    required_secrets = [_s]
})

_es = [m for m in _out.manifests if m.kind == "ExternalSecret" and m.metadata.name == "order-db-forge-ro"]
_gen = _out.required_secrets[0].source.generate

assert_name = _s.name == "order-db-forge-ro"
assert_keys = _s.keys == ["password", "username"]
assert_username_is_the_role = _s.source.templates["username"] == "forge_ro"
assert_password_generated_once_and_never_rotated = _gen["ORDER_DB_FORGE_RO_PASSWORD"].kind == "random" and _s.source.immutable == ["ORDER_DB_FORGE_RO_PASSWORD"] and _s.source.pin["ORDER_DB_FORGE_RO_PASSWORD"] == 1
assert_basic_auth = _s.source.secret_type == "kubernetes.io/basic-auth"
assert_one_external_secret_rendered = len(_es) == 1
assert_rendered_type = _es[0].spec.target.template.type == "kubernetes.io/basic-auth"
assert_rendered_reload_label = _es[0].spec.target.template.metadata.labels["cnpg.io/reload"] == "true"
assert_rendered_username = _es[0].spec.target.template.data.username == "forge_ro"
assert_pinned_to_one = _es[0].spec.data[0].remoteRef.version == "1"
