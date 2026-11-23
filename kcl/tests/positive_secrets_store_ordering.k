"""
Positive test — SF11: what the render says so the applier can order a
store-backed secret ahead of the workloads that read it.

  * every object the store source renders (the login ServiceAccount and token
    Role, the SecretStore, the ExternalSecret) carries
    `forge.dev/secret-stage: secrets`, so forge applies them, and waits for the
    ExternalSecret to sync, before any workload (pkg/release.ClassifySecretStage);
  * an ordinary ExternalSecret has no `forge.dev/secret-sync` annotation, so a
    deploy waits for it;
  * a `shadow` copy is annotated `shadow`: no workload reads it, so it gates
    nothing;
  * a store-delivered ControlPlaneSecret is annotated `deferred`: the control
    plane writes its value AFTER the deploy, so a deploy that waited for it would
    wait for a value that cannot exist yet.

The pin annotation on a consumer's pod template is stamped by Go, in the one
extraction hook, not here (internal/cluster/secret_pins.go).
"""

# kcl-args: env=prod

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {
    cluster = "gke_acme_prod"
    namespace = "acme-prod"
    connected_cluster = "acme-prod"
    platform = "amd64"
}

_hosted = forge.HostedStore {org = "55949942-3ee9-4cfc-bb7c-817bdba75186", env_id = "13b10699-40f1-4b19-b273-4b36a9977cc9"}

_out = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    external_secrets_operators = [forge.ExternalSecretsOperator {cluster = _prod}]
    control_plane_secrets = [forge.ControlPlaneSecret {
        name = "reliant-otel-auth"
        cluster = _prod
        delivery = "store"
        store = _hosted
    }]
    required_secrets = [
        forge.ExternalSecret {
            name = "db-role"
            namespace = "acme-prod"
            keys = ["password"]
            source = forge.StoreSource {
                store = _hosted
                data = {"password" = "RELIANT_DB_ROLE_PASSWORD"}
                pin = {"RELIANT_DB_ROLE_PASSWORD" = 2}
            }
        }
        forge.ExternalSecret {
            name = "migrating"
            namespace = "acme-prod"
            keys = ["token"]
            source = forge.StoreSource {
                store = _hosted
                data = {"token" = "SERVICE_TOKEN"}
                shadow = True
                creation_policy = "Merge"
            }
        }
    ]
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
})

_es = {x.metadata.name: x for x in _out.manifests if x.kind == "ExternalSecret"}
_store = [x for x in _out.manifests if x.kind == "SecretStore"][0]
_sa = [x for x in _out.manifests if x.kind == "ServiceAccount" and x.metadata.name == "reliant-secrets"][0]
_role = [x for x in _out.manifests if x.kind == "Role" and x.metadata.name == "reliant-secrets-token"][0]

_ann = lambda x: any -> {str: str} {
    x.metadata.annotations if "annotations" in x.metadata else {}
}

_staged = [_store, _sa, _role] + [_es[n] for n in _es]
assert_every_object_is_staged = len([x for x in _staged if x.metadata.labels["forge.dev/secret-stage"] == "secrets"]) == len(_staged)
assert_an_ordinary_secret_is_awaited = "forge.dev/secret-sync" not in _ann(_es["db-role"])
assert_the_pin_is_in_the_read = _es["db-role"].spec.data[0].remoteRef.version == "2"
assert_a_shadow_gates_nothing = _ann(_es["migrating-eso-shadow"])["forge.dev/secret-sync"] == "shadow"
assert_a_control_plane_secret_is_deferred = _ann(_es["reliant-otel-auth"])["forge.dev/secret-sync"] == "deferred"
