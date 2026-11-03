"""
Positive test — a forge.ExternalSecret with a StoreSource: data copies, a
template derives a DSN from them, a generated value, a pin, an immutable name,
and a Secret with no source beside it that says why.
"""

import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_us-central1_prod", namespace = "acme-prod", platform = "amd64"}

_bundle = forge.Bundle {
    project = "fixture"
    openbao = forge.OpenBao {}
    external_secrets_operators = [forge.ExternalSecretsOperator {cluster = _hub}]
    required_secrets = [
        forge.ExternalSecret {
            name = "app-db-role"
            namespace = "acme-prod"
            keys = ["password", "proxy-database-url"]
            reason = "role the proxy dials the database as"
            source = forge.StoreSource {
                store = forge.OpenBaoStore {}
                data = {"password" = "APP_DB_ROLE_PASSWORD"}
                templates = {"proxy-database-url" = "postgres://app:{{ .password | urlquery }}@127.0.0.1:5432/app?sslmode=disable"}
                generate = {"APP_DB_ROLE_PASSWORD" = forge.GeneratedSecret {kind = "password", length = 24}}
                pin = {"APP_DB_ROLE_PASSWORD" = 1}
                immutable = ["APP_DB_ROLE_PASSWORD"]
                refresh = "15m"
            }
        }
        forge.ExternalSecret {
            name = "signing"
            namespace = "acme-prod"
            keys = ["private-key", "public-key"]
            source = forge.StoreSource {
                store = forge.HostedStore {}
                data = {"private-key" = "SIGNING_KEY", "public-key" = "SIGNING_KEY_PUB"}
                generate = {"SIGNING_KEY" = forge.GeneratedSecret {kind = "rsa_2048", companion = "SIGNING_KEY_PUB"}}
                shadow = True
            }
        }
        forge.ExternalSecret {
            name = "gsm-token"
            namespace = "acme-prod"
            keys = ["token"]
            source = forge.StoreSource {
                store = forge.GCPSecretManagerStore {project = "acme-prod"}
                data = {"token" = "cloudflare-api-token"}
                creation_policy = "Merge"
            }
        }
        forge.ExternalSecret {name = "vendor-cert", namespace = "acme-prod", keys = ["tls.crt"], out_of_band_reason = "issued by the vendor portal"}
    ]
    control_plane = forge.ControlPlane {}
}

_s = _bundle.required_secrets

assert_template_reads_declared_data = _s[0].source.templates["proxy-database-url"] != ""
assert_password_kind = _s[0].source.generate["APP_DB_ROLE_PASSWORD"].kind == "password" and _s[0].source.generate["APP_DB_ROLE_PASSWORD"].length == 24
assert_pair_kind = _s[1].source.generate["SIGNING_KEY"].companion == "SIGNING_KEY_PUB"
assert_shadow = _s[1].source.shadow and not _s[0].source.shadow
assert_gsm_name_with_hyphens = _s[2].source.data["token"] == "cloudflare-api-token"
assert_out_of_band_reason = _s[3].out_of_band_reason == "issued by the vendor portal" and not _s[3].source
assert_default_refresh = _s[1].source.refresh == "1h" and _s[1].source.creation_policy == "Orphan"
assert_eso_pinned = _bundle.external_secrets_operators[0].version == "2.13.0" and "@sha256:" in _bundle.external_secrets_operators[0].image
assert_eso_context = _bundle.external_secrets_operators[0].cluster.cluster == "gke_acme_us-central1_prod"
