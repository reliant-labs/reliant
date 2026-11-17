"""
Positive tests for forge.KubeconfigSecret's SERVICE-ACCOUNT credential
mode — the mint that works for ANY cluster forge can reach through a
declared kubectl context, GKE included.

The other two reachability modes copy the OPERATOR's credential for the
target cluster out of their kubeconfig. That is fine for k3d, whose
kubeconfig carries a client certificate, and useless for GKE, whose
kubeconfig carries an `exec` plugin (gke-gcloud-auth-plugin) that only
resolves on a machine with gcloud and the operator's ADC. A pod holding
that kubeconfig cannot authenticate at all.

`service_account` says: don't copy a credential, MINT one. forge applies
a ServiceAccount, the declared rules as a ClusterRole (or a Role per
namespace), a binding, and a long-lived token Secret to the TARGET
cluster, then assembles a kubeconfig whose only credential is that
token, inline. No exec plugin, no token file, no client cert — readable
by any pod.

Exercises:
  * The service_account block projects through, rules included.
  * reachability = "in-cluster" (the consumer runs IN the target).
  * namespaces scopes the rules to Roles instead of a ClusterRole.
  * target_context carries a non-k3d context for the target cluster.
  * Still never a rendered manifest — the token is minted at deploy time.
"""

import forge

# The control-plane hub edge: a Flux controller in THIS cluster applying
# tier CRs and hosted-user namespaces to THIS cluster's API server.
_hub = forge.KubeconfigSecret {
    name = "control-plane-hub-kubeconfig"
    in_cluster = "gke_reliant-labs_us-central1_prod"
    target_cluster = "prod"
    target_context = "gke_reliant-labs_us-central1_prod"
    context_name = "hub"
    reachability = "in-cluster"
    service_account = forge.KubeconfigServiceAccount {
        name = "flux-hub-applier"
        namespace = "control-plane-prod"
        rules = [
            forge.KubeconfigPolicyRule {
                api_groups = ["forge.dev"]
                resources = ["*"]
                verbs = ["*"]
            }
            forge.KubeconfigPolicyRule {
                api_groups = [""]
                resources = ["namespaces", "serviceaccounts"]
                verbs = ["get", "list", "watch", "create", "update", "patch"]
            }
            forge.KubeconfigPolicyRule {
                api_groups = ["rbac.authorization.k8s.io"]
                resources = ["roles", "rolebindings"]
                verbs = ["get", "list", "watch", "create", "update", "patch"]
            }
            # A rule limited to named objects: `bind` on exactly two roles.
            forge.KubeconfigPolicyRule {
                api_groups = ["rbac.authorization.k8s.io"]
                resources = ["clusterroles"]
                verbs = ["bind"]
                resource_names = ["hub-reconciler", "hub-source"]
            }
        ]
    }
}

# A namespace-scoped variant: the same rules bound as Roles in two
# namespaces rather than cluster-wide.
_scoped = forge.KubeconfigSecret {
    name = "scoped-kubeconfig"
    in_cluster = "k3d-cp"
    target_cluster = "workload"
    target_context = "k3d-workload"
    context_name = "workload"
    reachability = "endpoint"
    service_account = forge.KubeconfigServiceAccount {
        name = "workload-reader"
        namespace = "forge-system"
        namespaces = ["app-a", "app-b"]
        rules = [
            forge.KubeconfigPolicyRule {
                resources = ["pods"]
                verbs = ["get", "list", "watch"]
            }
        ]
    }
}

_cp = forge.Cluster { name = "cp" }
_target = forge.ClusterTarget {
    cluster = "k3d-cp"
    namespace = "dev"
}
_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _target
    clusters = [_cp]
    kubeconfig_secrets = [_hub, _scoped]
    workloads = []
}

output = forge.render(_bundle)
manifests = forge.render(_bundle).manifests

# ── Invariants ─────────────────────────────────────────────────────

assert_count = len(output.kubeconfig_secrets) == 2

_h = output.kubeconfig_secrets[0]

# The consumer runs inside the target: server is the in-cluster address.
assert_hub_reach = _h.reachability == "in-cluster"
# A non-k3d context for the target — there is no `k3d-<name>` to derive.
assert_hub_target_context = _h.target_context == "gke_reliant-labs_us-central1_prod"

# The service_account block projects, with its rules.
assert_hub_sa_name = _h.service_account.name == "flux-hub-applier"
assert_hub_sa_ns = _h.service_account.namespace == "control-plane-prod"
assert_hub_rule_count = len(_h.service_account.rules) == 4
assert_hub_rule_groups = _h.service_account.rules[0].api_groups == ["forge.dev"]
assert_hub_rule_verbs = _h.service_account.rules[0].verbs == ["*"]
# resource_names projects through, and defaults to [] (every object).
assert_hub_rule_resource_names = _h.service_account.rules[3].resource_names == ["hub-reconciler", "hub-source"]
assert_hub_rule_resource_names_default = _h.service_account.rules[0].resource_names == []
# Cluster-wide: no namespaces => a ClusterRole.
assert_hub_cluster_wide = _h.service_account.namespaces == []

_s = output.kubeconfig_secrets[1]

# Namespace-scoped: the same rules become a Role in each namespace.
assert_scoped_namespaces = _s.service_account.namespaces == ["app-a", "app-b"]
# api_groups defaults to the core group.
assert_scoped_default_group = _s.service_account.rules[0].api_groups == [""]

# A declaration WITHOUT a service_account still projects null there, so
# the copy-the-operator's-credential path stays untouched.
_plain = forge.KubeconfigSecret {
    name = "plain"
    in_cluster = "k3d-cp"
    target_cluster = "workload"
    context_name = "workload"
}
_plain_bundle = forge.Bundle {
    project = "fixture"
    kubeconfig_secrets = [_plain]
    workloads = []
}
output_plain = forge.render(_plain_bundle)
assert_plain_no_sa = output_plain.kubeconfig_secrets[0].service_account == None
# target_context defaults to null; forge derives `k3d-<target_cluster>`.
assert_plain_no_target_context = output_plain.kubeconfig_secrets[0].target_context == None

# The minted Secret is NEVER a rendered manifest: its value (the SA
# token) does not exist until forge mints it against a live cluster.
assert_no_manifest = len([m for m in manifests \
    if m.kind == "Secret" and m.metadata.name == "control-plane-hub-kubeconfig"]) == 0
# Nor is the ServiceAccount / ClusterRole — those land on the TARGET
# cluster, which is not necessarily where this bundle's manifests apply.
assert_no_sa_manifest = len([m for m in manifests \
    if m.kind == "ServiceAccount" and m.metadata.name == "flux-hub-applier"]) == 0
