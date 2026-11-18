"""
Positive tests for the RenderedSecrets secret provider (dotenv-sourced).

RenderedSecrets declares named k8s Secrets EXPLICITLY (name + per-key
source). This file exercises the `from="file"` source (the env-agnostic
path — no inline values, so no `-D env` gate). The `from="literal"` dev/e2e
gating has its own env-bound test (positive_rendered_secrets_literal.k) and
negative test (negative_rendered_secrets_literal_prod.k).

KCL only emits the DECLARATION (name + per-key from/key) — for dotenv keys
the VALUE never enters KCL output. The declaration rides in the ENTITY
output alongside other providers, and is NEVER in output.manifests (forge
renders + applies the Secrets CLI-side, per cluster).
"""

import forge
import forge.workloads as fw

_target = forge.ClusterTarget {cluster = "k3d-dev", namespace = "dev"}

_bundle = forge.Bundle {
    project = "dev"
    workloads = [fw.Workload {
        name = "api"
        image = "localhost:5051/api"
        env = {DB_PASSWORD = forge.SecretRef {name = "db-credentials", key = "password"}}
        runtime = forge.OnCluster {target = _target}
    }]
    secret_provider = forge.RenderedSecrets {
        secrets = [
            forge.RenderedSecret {
                name = "db-credentials"
                keys = {
                    "password" = forge.RenderedSecretKey {from = "file", key = "DB_PASSWORD"}
                    "username" = forge.RenderedSecretKey {from = "file", key = "DB_USER"}
                }
            }
        ]
    }
}

output = forge.render(_bundle)

assert_type = output.secret_provider.type == "rendered"
assert_secret_name = output.secret_provider.secrets[0].name == "db-credentials"
assert_pw_from = output.secret_provider.secrets[0].keys.password.from == "file"
assert_pw_key = output.secret_provider.secrets[0].keys.password.key == "DB_PASSWORD"
assert_pw_no_value = output.secret_provider.secrets[0].keys.password.value == Undefined
# The workload's reference is what makes the provider place the Secret.
assert_workload_refs_secret = output.workloads[0].spec.env[0].secretRef.name == "db-credentials"
assert_no_secret_manifest = len([m for m in output.manifests if m.kind == "Secret"]) == 0
