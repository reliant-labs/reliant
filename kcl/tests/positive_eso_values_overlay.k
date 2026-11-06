"""
Positive test — `values` merges DEEPLY over the hardened posture (maps merge,
lists replace, last wins), and `extraObjects` APPENDS to the admission
policies. Overriding a hardening value is the 20% path; adding one object
must never cost the policies. Also: a k3d forge.Cluster is a valid cluster.
"""

import forge

_dev = forge.Cluster {name = "dev"}

output = forge.render(forge.Bundle {
    project = "fixture"
    env = "dev"
    clusters = [_dev]
    external_secrets_operators = [
        forge.ExternalSecretsOperator {
            cluster = _dev
            replicas = 1
            priority_class = "system-cluster-critical"
            values = {
                resources.limits.memory = "1Gi"
                log.level = "debug"
                extraObjects = [{apiVersion = "v1", kind = "ConfigMap", metadata = {name = "mine", namespace = "external-secrets"}}]
            }
        }
    ]
})

_c = output.helm_charts[0]
_v = _c.values

assert_k3d_context = _c.cluster == "k3d-dev" and not _c.cluster_target
assert_deep_merge_keeps_sibling = _v.resources.limits.memory == "1Gi" and _v.resources.limits.cpu == "500m" and _v.resources.requests.memory == "128Mi"
assert_new_key = _v.log.level == "debug"
assert_replicas = _v.replicaCount == 1 and _v.podDisruptionBudget.enabled == False
assert_priority = _v.priorityClassName == "system-cluster-critical"
assert_extra_appended = len(_v.extraObjects) == 7 and any o in _v.extraObjects { o.metadata.name == "mine" } and any o in _v.extraObjects { o.metadata.name == "eso-writers" }
assert_still_hardened = _v.rbac.serviceAccountTokenCreate == False and _v.webhook.create == False
