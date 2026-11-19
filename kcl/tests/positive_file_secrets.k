"""
Positive tests for FileSecrets — the scaffolded dev/local secret provider.

FileSecrets replaced DotenvSecrets as the default because a single
`KEY=value` file was injected into every host service WHOLESALE, so a
value went live everywhere without ever being declared in KCL. That made
the untracked file the cheapest place to put anything, and non-secret
config (service URLs, client IDs) drifted out of version control into it.

A directory (one file per secret, filename = env-var name) pairs with
declaration-scoped injection in the CLI: a service receives only the keys
it names via `EnvVar.secret_ref`, so a file nothing declares is inert.

Exercises:

  * FileSecrets projects {type:"dir", path:...} into the ENTITY output.
  * The declaration NEVER reaches the manifest stream.
  * RenderedSecretKey defaults to from="dir".
  * A RenderedSecrets bundle mixing dir + literal sources projects both.
"""

# kcl-args: env=dev | env=e2e
# reject-args: env=prod
# reject-expect: FileSecrets renders PLAINTEXT secrets

import forge
import forge.workloads as fw

_target = forge.ClusterTarget {
    cluster = "k3d-dev"
    namespace = "dev"
}

_dir_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _target
    workloads = [fw.Workload {
        name = "api"
        image = "localhost:5051/api:dev"
        runtime = forge.OnCluster {target = _target}
    }]
    secret_provider = forge.FileSecrets {
        path = "secrets/dev.yaml"
    }
}

# A rendered provider whose keys mix the two legitimate dev sources: a
# per-developer credential read from the secret dir, and a fixed value
# that is identical on every machine (so it belongs in version control,
# not in anyone's local store).
_rendered_bundle = forge.Bundle {
    project = "fixture"
    workloads = []
    secret_provider = forge.RenderedSecrets {
        secrets = [forge.RenderedSecret {
            name = "app-secrets"
            keys = {
                "stripe_secret_key" = forge.RenderedSecretKey {key = "STRIPE_SECRET_KEY"}
                "nats_password" = forge.RenderedSecretKey {from = "literal", value = "dev-nats"}
            }
        }]
    }
}

output_store = forge.render(_dir_bundle)
output_rendered = forge.render(_rendered_bundle)
manifests_dir = forge.render(_dir_bundle).manifests

# ── Invariants ─────────────────────────────────────────────────────

# FileSecrets projects type + path into the ENTITY output.
assert_store_type = output_store.secret_provider.type == "file"
assert_store_path = output_store.secret_provider.path == "secrets/dev.yaml"

# The path is a DIRECTORY, never a .env* file — the whole point.
assert_not_dotenv = not output_store.secret_provider.path.startswith(".env")

# Values never enter the manifest stream from a provider declaration.
assert_no_secret_manifest = len([m for m in manifests_dir if m.kind == "Secret"]) == 0

# from defaults to "dir": the common case needs no `from` at all, so the
# deprecated dotenv source can't be reached by accident.
assert_default_from = output_rendered.secret_provider.secrets[0].keys["stripe_secret_key"]["from"] == "file"
assert_dir_key = output_rendered.secret_provider.secrets[0].keys["stripe_secret_key"].key == "STRIPE_SECRET_KEY"

# A literal rides alongside it for values that are the same everywhere.
assert_literal_from = output_rendered.secret_provider.secrets[0].keys["nats_password"]["from"] == "literal"
assert_literal_value = output_rendered.secret_provider.secrets[0].keys["nats_password"].value == "dev-nats"
