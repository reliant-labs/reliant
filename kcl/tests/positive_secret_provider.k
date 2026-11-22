"""
Positive tests for the bundle-level secret PROVIDER declaration.

A secret provider declares WHERE secret values come from for one env.
KCL only emits the DECLARATION (type + path) — secret VALUES never
enter KCL render output. The declaration rides in the ENTITY output
(forge.render) alongside `services`, and is ABSENT (not null) when the
bundle declares no provider. It is NEVER emitted into the k8s manifest
stream (output.manifests).

Exercises:

  * FileSecrets projects {type:"dir", path:...} into output.
  * ExternalSecrets projects {type:"external"} into output.
  * No provider => `secret_provider` key absent from output.
  * Provider declaration never appears in output.manifests.
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

_store_bundle = forge.Bundle {
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

_external_bundle = forge.Bundle {
    project = "fixture"
    workloads = []
    secret_provider = forge.ExternalSecrets {}
}

_no_provider_bundle = forge.Bundle {
    project = "fixture"
    workloads = []
}

output_store = forge.render(_store_bundle)
output_external = forge.render(_external_bundle)
output_none = forge.render(_no_provider_bundle)
manifests_store = forge.render(_store_bundle).manifests

# ── Invariants ─────────────────────────────────────────────────────

# FileSecrets projects type + path into the ENTITY output.
assert_store_type = output_store.secret_provider.type == "file"
assert_store_path = output_store.secret_provider.path == "secrets/dev.yaml"

# ExternalSecrets projects type into the ENTITY output (no path).
assert_external_type = output_external.secret_provider.type == "external"

# No provider => key ABSENT from the entity output (not null garbage).
assert_none_absent = "secret_provider" not in output_none

# The provider declaration NEVER appears in the manifest stream — no
# Secret manifest is rendered from a provider declaration by KCL.
assert_no_secret_manifest = len([m for m in manifests_store if m.kind == "Secret"]) == 0
