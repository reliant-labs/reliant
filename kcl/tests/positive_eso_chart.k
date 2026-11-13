"""
Positive test — forge.ExternalSecretsOperator lowers to ONE hardened
forge.HelmChart on its cluster, plus the admission policies.

WHAT THIS PINS
  * the chart reference, version and the CRD bundle forge supplies itself;
  * `installCRDs = false`: chart 2.13's CRDs are templates gated on it, so
    `--skip-crds` alone would still emit them;
  * every unused kind is off (process*, crds.create*), the webhook and cert
    controller are off with conversion off (P1), token creation, aggregation
    and servicebindings are off;
  * the image is pinned by digest, caching of all Secrets is off;
  * the cluster is a ClusterTarget, projected as its kubectl context;
  * the three VAPs and their bindings ride extraObjects, `Deny`, fail closed;
  * the release-shape record (`external_secrets_operators`) names the cluster.
"""

import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_us-central1_prod", namespace = "acme-prod", platform = "amd64", connected_cluster = "hub"}
_dev = forge.Cluster {name = "dev"}

output = forge.render(forge.Bundle {
    project = "fixture"
    env = "prod"
    cluster_target = _hub
    external_secrets_operators = [
        forge.ExternalSecretsOperator {
            cluster = _hub
            gcp_service_account = "eso@acme-prod.iam.gserviceaccount.com"
            admission = forge.ESOAdmission {
                writers = ["deployer@acme-prod.iam.gserviceaccount.com"]
                servers = ["https://openbao.openbao.svc:8200"]
                jwt_mounts = ["jwt-hub"]
                audiences = ["https://secrets.example.com/c/hub"]
                gcp_projects = ["acme-prod"]
                scrape_namespaces = ["monitoring"]
            }
        }
    ]
})

_c = output.helm_charts[0]
_v = _c.values
_objs = _v.extraObjects
_by = lambda kind: str, name: str -> any { [o for o in _objs if o.kind == kind and o.metadata.name == name][0] }

assert_one_chart = len(output.helm_charts) == 1 and _c.name == "external-secrets"
assert_chart_ref = _c.oci == "oci://ghcr.io/external-secrets/charts/external-secrets" and _c.version == "2.13.0"
assert_crd_bundle = _c.crds == "external-secrets"
assert_cluster_is_target_context = _c.cluster == "gke_acme_us-central1_prod" and _c.cluster_target == True
assert_namespace_psa = _c.namespace == "external-secrets" and _c.namespace_labels["pod-security.kubernetes.io/enforce"] == "restricted"

# CRDs: forge supplies them, the chart does not.
assert_chart_crds_off = _v.installCRDs == False
assert_unused_crds_off = not _v.crds.createClusterExternalSecret and not _v.crds.createClusterSecretStore and not _v.crds.createPushSecret and not _v.crds.createClusterPushSecret and not _v.crds.createClusterGenerator
assert_conversion_off = _v.crds.conversion.enabled == False and _v.crds.unsafeServeV1Beta1 == False
assert_unused_kinds_not_processed = not _v.processClusterStore and not _v.processClusterExternalSecret and not _v.processPushSecret and not _v.processClusterPushSecret and not _v.processClusterGenerator

# P1: webhook and cert controller off, which the chart pairs with conversion off.
assert_webhook_off = _v.webhook.create == False and _v.certController.create == False
assert_webhook_ciphers = "CHACHA20_POLY1305" in _v.webhook.tls.ciphers and "CBC" not in _v.webhook.tls.ciphers

# RBAC.
assert_no_token_create = _v.rbac.serviceAccountTokenCreate == False
assert_no_aggregation = not _v.rbac.aggregateToView and not _v.rbac.aggregateToEdit and not _v.rbac.aggregateToAdmin
assert_no_servicebindings = _v.rbac.servicebindings.create == False

# Supply chain + caching.
assert_image_digest = "@sha256:" in _v.image.tag and _v.image.repository == "ghcr.io/external-secrets/external-secrets"
assert_no_secret_cache = _v.extraArgs["enable-secrets-caching"] == "false" and _v.extraArgs["enable-configmaps-caching"] == "false" and _v.extraArgs["enable-managed-secrets-caching"] == "true"
assert_generic_targets_off = _v.extraArgs["unsafe-allow-generic-targets"] == "false" and _v.extraArgs["enable-flood-gate"] == "true"
assert_ha = _v.replicaCount == 2 and _v.leaderElect == True
assert_gke_wi = _v.serviceAccount.annotations["iam.gke.io/gcp-service-account"] == "eso@acme-prod.iam.gserviceaccount.com"
assert_owner_label = _v.commonLabels["forge.dev/component-owner"] == "fixture.prod"

# Network.
assert_netpol = _v.networkPolicy.enabled == True and len(_v.networkPolicy.egress) == 2
assert_scrape_ingress = _v.networkPolicy.ingress[0].ports[0].port == 8080

# Admission.
assert_three_policies = sorted([o.metadata.name for o in _objs if o.kind == "ValidatingAdmissionPolicy"]) == ["eso-es-policy", "eso-store-pin", "eso-writers"]
assert_three_bindings = sorted([o.metadata.name for o in _objs if o.kind == "ValidatingAdmissionPolicyBinding"]) == ["eso-es-policy", "eso-store-pin", "eso-writers"]
assert_deny = all b in [o for o in _objs if o.kind == "ValidatingAdmissionPolicyBinding"] { b.spec.validationActions == ["Deny"] }
assert_fail_closed = all p in [o for o in _objs if o.kind == "ValidatingAdmissionPolicy"] { p.spec.failurePolicy == "Fail" }
assert_writers_expression = "'deployer@acme-prod.iam.gserviceaccount.com'" in _by("ValidatingAdmissionPolicy", "eso-writers").spec.validations[0].expression
assert_store_pin_server = "'https://openbao.openbao.svc:8200'" in _by("ValidatingAdmissionPolicy", "eso-store-pin").spec.validations[0].expression

# Release shape record.
assert_record = output.external_secrets_operators == [{
    cluster = "gke_acme_us-central1_prod"
    connected_cluster = "hub"
    namespace = "external-secrets"
    version = "2.13.0"
}]
