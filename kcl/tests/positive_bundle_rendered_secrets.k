"""
Bundle.rendered_secrets — Secrets forge renders ITSELF, independent of
secret_provider, placed EXPLICITLY.

The shape this exists for (control-plane dev): services read a FileSecrets
store, and an in-cluster store (OpenBao) runs from PLAIN manifests that
mount three Secrets no service references. A Bundle has one secret_provider,
and a RenderedSecrets provider lands a Secret only where a SERVICE names it,
so neither could place them.

Env-gated (a literal key): runs under `-D env=dev|e2e` and is REJECTED under
prod (the kcl-args / reject-args directives).
"""

# kcl-args: env=dev | env=e2e
# reject-args: env=prod
# reject-expect: only allowed in dev/e2e

import forge
import forge.workloads as fw

_target = forge.ClusterTarget {
    cluster = "k3d-cp"
    namespace = "cp-dev"
}

_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _target
    workloads = [fw.Workload {
        name = "api"
        image = "localhost:5051/api:dev"
        runtime = forge.OnCluster {target = _target}
    }]
    secret_provider = forge.FileSecrets {path = "secrets/dev.yaml"}
    rendered_secrets = [
        # Placement defaults to cluster_target.
        forge.RenderedSecret {
            name = "openbao-storage"
            keys = {"connection-url" = forge.RenderedSecretKey {from = "literal", value = "postgres://postgres:postgres@host.k3d.internal:5434/openbao"}}
        }
        # A store key: only the KEY projects, never a value.
        forge.RenderedSecret {
            name = "openbao-unseal"
            keys = {"static-key" = forge.RenderedSecretKey {key = "OPENBAO_STATIC_KEY"}}
        }
        # Explicit placement on another cluster.
        forge.RenderedSecret {
            name = "openbao-unseal"
            cluster = "k3d-cp-daemon"
            namespace = "workload"
            keys = {"static-key" = forge.RenderedSecretKey {key = "OPENBAO_STATIC_KEY"}}
        }
    ]
}

output = forge.render(_bundle)
manifests = forge.render(_bundle).manifests

_rs = output.rendered_secrets

# The provider is untouched: rendered_secrets rides BESIDE it.
assert_provider_is_file = output.secret_provider.type == "file"
assert_three_declared = len(_rs) == 3

# Placement is RESOLVED in the render — the CLI never infers it.
assert_default_cluster = _rs[0].cluster == "k3d-cp"
assert_default_namespace = _rs[0].namespace == "cp-dev"
assert_explicit_cluster = _rs[2].cluster == "k3d-cp-daemon"
assert_explicit_namespace = _rs[2].namespace == "workload"

# A store key carries its KEY and no value; a literal carries its value.
assert_file_key = _rs[1].keys["static-key"].key == "OPENBAO_STATIC_KEY"
assert_file_no_value = _rs[1].keys["static-key"].value == Undefined
assert_literal_value = _rs[0].keys["connection-url"].value == "postgres://postgres:postgres@host.k3d.internal:5434/openbao"

# forge applies these CLI-side: they never enter the manifest stream.
assert_no_secret_manifest = len([m for m in manifests if m.kind == "Secret"]) == 0
