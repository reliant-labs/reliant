---
name: cluster-connect
description: Connect a Kubernetes cluster you operate to the control plane so environments can deploy into it — the one auth (an uploaded, scoped ServiceAccount token), the address and CA overrides, the grant derived from what the hub applies, delivery, the issuer, disconnect, and the KCL binding.
---

# Connecting a cluster you operate

`forge cluster connect` registers a cluster **by address**. Nothing is
installed in it beyond a dedicated ServiceAccount and the RBAC the platform's
apply needs, and the cluster keeps existing after you disconnect.

```bash
forge cluster connect prod-us --context gke_acme_us-central1_prod --env prod
```

forge reads the API server address and CA (from your kubectl context, or `--address` /
`--ca-file`), uses **your own context** to create a dedicated ServiceAccount and its
least-privilege RBAC, reads that ServiceAccount's token, and uploads it to the control
plane **write-only**. The control plane then authenticates to your cluster as that
ServiceAccount and nothing else.

**There is one auth, on any Kubernetes cluster** — GKE, EKS, AKS, Vultr VKE, k3s, bare
metal. No cloud identity of ours is trusted in your project, there is no IAM grant for
you to run, and no `--auth` flag.

**Declarative and idempotent by name.** Re-running `connect` with the same name
UPDATES the cluster rather than colliding, so fixing a rotated endpoint is the
same command again, and the command is safe to put in a script.

`--env` names **which control plane** to talk to, from that env's
`forge.ControlPlane` declaration. Your organization comes from the credential
and is never sent.

## The address the control plane dials

The hub dials from its own pods, so the address it needs is not always the one your
laptop uses.

| Source | When |
|---|---|
| `--address https://host[:port]` | Always wins. Port **443 or 6443** (6443 is the k3s and kubeadm default); any other port is refused. |
| GKE's **public** endpoint and CA | A `gke_<project>_<location>_<cluster>` context, read with `gcloud container clusters describe`. The private endpoint is never chosen for you: pass it with `--address` if the control plane reaches the cluster over a peered network. |
| the kubeconfig's `cluster.server` | Everything else. |

The CA is `--ca-file <pem>` if given, else GKE's `masterAuth` CA, else the
kubeconfig's. With none, forge refuses: it never skips verification.

A GKE **DNS endpoint** (`https://….gke.goog`, which a modern `get-credentials` writes
and which carries no CA) is **refused**. It is IAM-gated, so it authenticates a Google
identity and a ServiceAccount token cannot pass it. Pass the public endpoint with
`--address`.

**The control plane refuses private, VPC, link-local and metadata addresses** (RFC 1918,
`169.254/16`, `*.internal`, …) for an organization that is not a platform org, and
re-checks on every deploy. A cluster whose only reachable address is private cannot be a
customer connect target.

## A binding is one env on one cluster, with its own identity and a derived grant

`connect` takes `--env`, and that env's binding on this cluster gets **its own
ServiceAccount** (`forge-system/forge-connect-<cluster>-<env>`) — nothing is shared across
envs or clusters. The apply runs **as that ServiceAccount**: no impersonation and no
bootstrap writes. The only objects connect writes in your cluster are the grant itself,
that ServiceAccount, its token Secret, and the delivery grant if asked.

### The grant is what the HUB applies, and nothing else

The rules are derived from the env's render, **only for the delivery units the control
plane's hub converges** (`bundle.hub_converges`, see `deploy/flux`). Units that
`forge env deploy` applies itself — `bundle.applier = "forge"`, or any unit left out of
`hub_converges` — contribute no rules.

- An env where **forge applies everything** gets an **empty apply grant**. `connect` still
  registers the cluster, and says so: *"the hub applies nothing on this cluster for env X:
  forge applies it"*. The binding then holds only the JWKS read (when an issuer is
  registered) and any `--delivery-secret` grant. `--dry-run` shows the empty diff.
- Move a unit to the hub (`hub_converges = [...]`) and **re-run connect**: the new rules
  appear. A release that adds a kind is **Forbidden by your cluster's own access check**
  until you re-run it — a tripwire, not a policy engine.

### You choose where; the render chooses what

`--apply-scope` is the owner's decision.

| `--apply-scope` | Grant |
|---|---|
| `namespace:<ns>[,<ns>…]` (the default, over the namespaces the render targets) | A **Role + RoleBinding in each namespace**. A rendered `Namespace` object is a name-pinned ClusterRole (`get`, `patch`, `update`; never `create` or `delete`). **Refused** when the render has cluster-scoped kinds (CRDs, ClusterRoles…) or objects outside the list. |
| `cluster` | A **ClusterRole + ClusterRoleBinding** over every namespace. forge prints a warning every time. |

Every rule set also holds `get/list/watch` on the ReplicaSets and Pods of any workload kind
in the render, which Flux's health check reads.

### `escalate` and `bind` are never unpinned; `impersonate` is never granted

An identity that holds `escalate` or `bind` without `resourceNames` can grant itself
anything. So, in **both** scopes, when the render carries RBAC:

- `escalate` on `roles` / `clusterroles` is pinned to the exact Role / ClusterRole names
  the render **declares**;
- `bind` is pinned to the exact names its RoleBindings and ClusterRoleBindings **point at**
  (in namespace scope, `bind` on a ClusterRole is pinned inside that namespace's Role).

A render whose RBAC objects use `generateName` is **refused** (its name is unknown, so it
cannot be pinned). Cluster scope can still rewrite the roles it is named for, which is why
the warning remains. No scope ever grants `impersonate`, and there is no namespace-wide
impersonation of any ServiceAccount.

### Review before you widen: `--dry-run`

```bash
forge cluster connect prod-us --context ... --env prod --apply-scope cluster --dry-run
forge cluster connect prod-us --context ... --env prod --dry-run --live-from live.yaml   # offline
```

`--dry-run` contacts no control plane and writes nothing. It renders the env,
reads the binding's live RBAC (read-only, through `--context`, or from a capture
with `--live-from`; the selector to capture with is printed), and prints
`+` new, `~` changed, `-` removed (a re-run prunes it), `=` unchanged — one line
per rule — then **the permissions this run newly grants**, then the exact
objects it would apply.

A re-run prunes this binding's grant objects the new plan no longer names, so
narrowing the scope narrows the cluster. It also removes the legacy
pre-binding grant (the shared identity with impersonate and bootstrap writes).

## The token, rotation and revocation

forge creates, server-side-applied in your cluster, per binding:

```
Namespace       forge-system                          (shared, left in place)
ServiceAccount  forge-system/forge-connect-<cluster>-<env>
Secret          forge-system/forge-connect-<cluster>-<env>-token   (kubernetes.io/service-account-token)
```

The token is **sent write-only and never printed** — not in the summary, not on failure,
not in `--dry-run`. It is a long-lived Secret token rather than a bounded `TokenRequest`:
nothing renews a bounded token, so a cluster nobody re-connects for 90 days would stop
deploying. Its blast radius is the grant above.

- **Rotate** by deleting the token Secret
  (`kubectl -n forge-system delete secret forge-connect-<cluster>-<env>-token`) and
  re-running `connect`: it recreates the Secret, reads the freshly minted token and
  uploads it. A plain re-run re-uploads the existing token.
- **Revoke** with `forge cluster disconnect` (below), which deregisters the cluster and
  deletes the ServiceAccount and Secret, killing the token.

## Secret delivery: opt-in, named, least privilege

Some Secrets are not deployed with a bundle but supplied and renewed by the
control plane (a `forge.ControlPlaneSecret`). Connect grants write access to
exactly those and nothing else, only when you name them:

```bash
forge cluster connect prod-us --context ... --env prod \
  --delivery-secret control-plane-prod/reliant-otel-auth \
  [--delivery-subject user:delivery@acme.iam.gserviceaccount.com]
```

- Per declared namespace, a **Role** (never a ClusterRole, whose `resourceNames`
  would apply in every namespace) allows `get/update/patch/delete` pinned to the
  named Secrets, plus `create`.
- RBAC cannot pin `create` to a name, so a **ValidatingAdmissionPolicy**
  (fail-closed, matched by exact username) admits writes from the delivery
  identities only for the declared `namespace/name` pairs, and only as type
  `Opaque` — otherwise a `service-account-token` Secret would mint a credential.
- `--delivery-subject` adds a second, dedicated identity beside the connect one.
- The policy is applied in its own step before the Roles, so a cluster without
  ValidatingAdmissionPolicy (Kubernetes < 1.30) fails before the create grant lands.
- Re-running connect re-converges it and **only adds**: a narrower re-run
  replaces the policy at once but leaves old delivery Roles for you to prune.
  `disconnect` removes all of it. `--dry-run` prints every object.

## The secret-store issuer: registered at connect, refreshed by re-running it

A cluster that reads store-backed Secrets (`forge.StoreSource`) logs in to the
secret store with a token its own API server signed. The store verifies it
against **that cluster's keys**, so connect registers the cluster's
service-account issuer with the control plane (secrets design §2.2). Nothing is
stored in your cluster and the store never calls back into it.

```bash
forge cluster connect prod-us --context ... --env prod                    # --issuer auto
forge cluster connect k3d-dev --context k3d-dev --env dev --issuer upload  # a cluster the store cannot reach
```

| `--issuer` | What happens |
|---|---|
| `auto` (default) | `discover` on a GKE context, `upload` elsewhere. If the issuer cannot be read, or the control plane declines it, the cluster **still connects for deploys** and connect prints why nothing was registered. |
| `discover` | The store fetches `<issuer>/.well-known/openid-configuration` itself. For an issuer reachable from the internet (GKE's is). |
| `upload` | forge reads `/.well-known/openid-configuration` and `/openid/v1/jwks` through `--context` and sends the key set. For a private cluster or k3d. A failure is an error, before anything is written. |
| `skip` | Leaves the registration (and the grant) as it is. |
| `withdraw` | Removes it; the store stops trusting the cluster. |

- **Public keys only.** A key set holding a private or symmetric member is never
  sent (the control plane refuses one too).
- **An upload is a snapshot; re-run connect to follow a key rotation.** The same
  command re-reads the set and replaces the registration; connect says whether
  the set is unchanged or REPLACED. A cluster nobody re-connects keeps trusting
  its old keys, and a login with a new key fails until you do.
- **The grant gains `get` on `/openid/v1/jwks`** (a non-resource URL, so it sits in
  the binding's ClusterRole even at namespace scope), shown by `--dry-run`.
- **The store's id for the cluster is the control plane's to assign** and is
  unique across all organizations: the first to register a name owns it. Connect
  prints it (`store id`, and the auth mount `jwt-<id>`) and keeps it in
  `.forge/state/cluster-issuer-<name>.json`. If it is not the name you gave,
  use it as `connected_cluster`: a SecretStore renders its mount and audience from
  that name.
- **k3s / k3d** signs with `https://kubernetes.default.svc.cluster.local`, which
  the control plane refuses (a `.local` host). A k3d cluster forge creates is
  already started with the public issuer first (the `.cluster.local` issuer and
  audience stay accepted for in-cluster tokens); a cluster created before that,
  or by hand, needs it. Start the API server with
  `--kube-apiserver-arg=service-account-issuer=https://kubernetes.default.svc`
  (k3d: `--k3s-arg "--kube-apiserver-arg=service-account-issuer=https://kubernetes.default.svc@server:*"`).

## Binding an environment to it

Registering a cluster does not point anything at it. An environment names it:

```python
_prod = forge.ClusterTarget {
    cluster = "gke_acme_us-central1_prod"   # the kubectl context: how YOUR machine reaches it
    namespace = "acme-prod"
    connected_cluster = "prod-us"           # the registered name: how the PLATFORM reaches it
}
```

Two fields for the same physical cluster, through two different authorities.
`cluster` is a kubectl context — a local fact that differs between laptops and
does not exist in CI. `connected_cluster` is an org-level resource holding the
address, CA and credential the platform deploys with. A control-plane-backed env
targeting a real cluster needs both.

forge sends these as `cluster_bindings` on `EnsureEnvironment`, resolving each
name to its id. **Unset on a non-k3d cluster in an env that declares a control
plane is refused at render**, naming `forge cluster connect` — because the
alternative is a deploy that is accepted and never lands, surfacing later as a
Flux credential error two layers from the missing field.

A k3d cluster needs none of this: forge applies to it directly and the control
plane never dials it.

## Verifying a connect: `list` and `show`

`connect` prints what it sent. To see what the control plane **now holds**, read
it back — the same client and `--env` as `connect`, read-only:

```bash
forge cluster list --env prod              # every connected cluster of the org
forge cluster show prod-us --env prod      # one, by exact name (GetCluster)
forge cluster show prod-us --env prod --json
```

Per cluster: name, id, **state** (`CONNECTED` — the control plane holds usable
connection material; `WAITING` — it holds nothing to try with; `STALE` — the
token's expiry has passed), auth (`token`, the only mode), the registered
**address** and **CA fingerprint**, the **issuer** registration (`discover` or
`upload` with its key count, and the store id), the **environments bound** to it
(by name, or by id when the caller may not read environments), and created/updated
times. A member the control plane does not report prints as `-` (null in JSON).

After a connect, check: `state` is `CONNECTED`, `address` is the one you meant, the
issuer is registered if the cluster reads store-backed Secrets, and the env you
bound appears under `envs`.

`--json` is one document with a `schema` field — `forge.cluster.list/v1` or
`forge.cluster.show/v1` — whose members are stable under that name and
present-and-null when absent, so a script can index them. **No token or
certificate is ever printed**: the control plane returns neither, and forge has no
output field that could carry one.

## Disconnect

```bash
forge cluster disconnect prod-us --env prod --context gke_acme_us-central1_prod
```

Two halves, in this order: the control plane revokes the credential first (its
registered issuer goes with the cluster), then
forge deletes everything it created for this cluster, **found by label** (every
binding's Roles, ClusterRoles and bindings, each token ServiceAccount and
Secret, and the delivery policy). A token revoked server-side is already
powerless, so a cleanup that then fails leaves litter rather than a live
credential.

**Refused while any live environment targets the cluster** — that check is the
control plane's, which is why this is not a local delete.

A missing object is not an error, so a disconnect run twice converges. The
`forge-system` **namespace is left in place**: it is shared by construction and
deleting a namespace deletes whatever else is in it.

Without `--context`, forge deregisters the cluster and tells you which objects
to remove by hand.

## Refusals you may hit

| Message | Why |
|---|---|
| `a GKE DNS endpoint (*.gke.goog)` | IAM-gated: a ServiceAccount token cannot pass it. Pass the cluster's public endpoint with `--address`. |
| `on port 8443` | The hub dials the API server on 443 or 6443 only. Expose it there (a load balancer in front) and pass `--address`. |
| `whose host is a loopback or bind address` | The hub dials from a **pod**, where `127.0.0.1` is that pod. The deploy would fail as "connection refused" against a healthy cluster. Connect by an address reachable from outside the cluster. |
| `https only` / `carries credentials in the URL` | Any other scheme, or userinfo in the URL, would leak the credential. |
| `no certificate authority … cannot verify` | forge never falls back to skipping verification. Pass `--ca-file <pem>`, or re-run your provider's `get-credentials`. |
| `the env's render does not fit namespace scope` | The render has cluster-scoped kinds or objects outside your namespaces. Widen the list, or choose `--apply-scope cluster`. |
| `uses generateName` | An RBAC object in the render has no fixed name, so `escalate`/`bind` cannot be pinned to it. Give it a `metadata.name`. |
| `applies nothing to this cluster` | The env's KCL does not bind this context at all. Set `ClusterTarget.cluster` / `connected_cluster` first. (An env that binds it but applies it with forge is **not** this error: it registers with an empty apply grant.) |
| `resolve the credential's organization` | forge checks the credential against the control plane before it writes anything to your cluster. Authenticate (signed in to Reliant: nothing to do — `reliant forge` / an agent shell uses your session; standalone: `forge login`; CI: the env's `token_env`) and re-run; the token needs `deploy:read`. |
| the control plane refuses the address (private / metadata range) | See "The address the control plane dials". |

A Secret the control plane writes and renews INTO a connected cluster (the OTLP ingest token) is declared with `forge.ControlPlaneSecret`: `deploy/control-plane-secret`.
