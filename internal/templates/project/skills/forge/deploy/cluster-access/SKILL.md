---
name: cluster-access
description: Reaching another cluster's API server from a workload — forge.KubeconfigSecret, the two credential models (mint a ServiceAccount vs reuse your own), address modes, and rotation on shared clusters.
---

# Reaching another cluster's API server

A workload that talks to a Kubernetes API server — a Flux controller, an
operator, a proxy — needs a kubeconfig in a Secret. `forge.KubeconfigSecret`
mints one on every deploy, so nothing stale is ever committed.

There are two credential models, and picking the wrong one produces a
kubeconfig that renders fine and cannot authenticate.

**Copying (the default).** With no `service_account`, forge copies the
credential from the target's own kubeconfig. That works for k3d, whose
kubeconfig carries a client certificate usable from anywhere:

```kcl
forge.KubeconfigSecret {
    name = "workload-kubeconfig"
    in_cluster = _cp.context        # where the Secret lands
    target_cluster = "workload"     # the cluster to reach (k3d)
    context_name = "workload"
}
```

**Minting (any cluster, including GKE).** A managed cluster's kubeconfig
authenticates with an `exec` plugin — "run `gke-gcloud-auth-plugin` using the
operator's credentials." Copy that into a Secret and the pod holding it has
no plugin binary and no credentials, so it cannot authenticate at all.
Declare `service_account` and forge mints a credential instead: a
ServiceAccount, the rules you declare, and a long-lived token, all created on
the TARGET cluster, projected into a kubeconfig whose only credential is that
token, inline.

```kcl
forge.KubeconfigSecret {
    name = "hub-kubeconfig"
    in_cluster = "gke_acme_us-central1_prod"     # where the Secret lands
    target_cluster = "prod"                       # label for the target
    target_context = "gke_acme_us-central1_prod"  # the context addressing it
    context_name = "hub"
    reachability = "in-cluster"                   # the reader is a pod HERE
    service_account = forge.KubeconfigServiceAccount {
        name = "flux-hub-applier"
        namespace = "acme-prod"
        rules = [
            forge.KubeconfigPolicyRule {
                api_groups = ["forge.dev"], resources = ["*"], verbs = ["*"]
            }
            forge.KubeconfigPolicyRule {
                api_groups = [""]
                resources = ["namespaces", "serviceaccounts"]
                verbs = ["get", "list", "watch", "create", "update", "patch"]
            }
        ]
    }
}
```

`reachability` says where the READER sits, which is what picks the
kubeconfig's `server`:

| value | the reader is | server |
|---|---|---|
| `in-network` (default) | a pod on the shared docker network | the k3d container by name, verified against the cluster CA |
| `endpoint` | outside the target | the target's own advertised endpoint |
| `in-cluster` | a pod INSIDE the target | `https://kubernetes.default.svc` |

`in-cluster` requires `service_account`: a pod cannot present the operator's
credential whatever the address.

Limit a rule to named objects with `resource_names` (rendered as `resourceNames`):

```kcl
forge.KubeconfigPolicyRule {
    api_groups = ["rbac.authorization.k8s.io"]
    resources = ["clusterroles"]
    verbs = ["bind"]
    resource_names = ["hub-reconciler", "hub-source"]
}
```

Kubernetes cannot restrict `list`, `watch`, `create` or `deletecollection` by
name, so do not rely on `resource_names` to bound those.

Scope the grant with `namespaces` on the ServiceAccount — the rules become a
Role in each listed namespace instead of a cluster-wide ClusterRole. Prefer
that whenever the consumer's reach is known.

Three things worth knowing before you rely on it:

- **Rotation.** The token is long-lived (a `kubernetes.io/service-account-token`
  Secret, not a TokenRequest), so it never expires between deploys. To rotate,
  delete the token Secret on the target and deploy. A bounded TokenRequest
  would tie validity to deploy cadence, so an env nobody deploys for a month
  would stop working — bound the credential with `rules` instead.
- **Shared clusters.** Everything forge creates is labelled
  `app.kubernetes.io/managed-by: forge`, and forge REFUSES to adopt an
  existing object without it rather than rewrite someone else's permissions.
- **Review before granting.** `forge env deploy --dry-run` prints every
  object the mint would create, on which cluster, without contacting one.

## Not this: letting the CONTROL PLANE deploy into your cluster

This skill is about a workload of yours reaching a cluster's API server. If
what you want is for the hosted control plane to deploy INTO a cluster you
operate, that is `forge cluster connect` — a different mechanism with a
different credential model (GKE workload identity, or a scoped ServiceAccount
token forge mints), and it needs no `KubeconfigSecret` at all.

Load `deploy/cluster-connect`.
