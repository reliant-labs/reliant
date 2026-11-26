---
name: deploy/openbao
description: Running a self-run secret store with forge.OpenBao — what the component renders, how its clients authenticate (JWT only), the ordering of an ADOPTION of a live store (ship every consumer's JWT flip in the same release), the HA node spread, and the failure windows each step costs. Use when you declare Bundle.openbao, when you replace a hand-run OpenBao with the component, when you plan a rollout that moves a store's clients, or when two store pods land on one node.
---

# `forge.OpenBao`: the store and its clients

```python
openbao = forge.OpenBao {}      # Raft x3, TLS, static seal, audit on, JWT auth
```

One component per cluster. It renders the server (a StatefulSet for Raft, a
Deployment for Postgres storage), its Services, certificate and CA ConfigMap, a
PodDisruptionBudget, a pre-rollout `widen` Job and a post-rollout `converge` Job.
Bootstrap values (seal key, database password, `operator init` output) are
`deploy/openbao-bootstrap`. Publishing it to other clusters is
`deploy/openbao-public`. This skill is the part that decides whether a rollout
hurts.

## Native complete-snapshot identity

The provider compiles one atomic consumer named `openbao-<namespace>-<name>` on
its exact cluster. That reserved name must not collide with an authored workload
or manifest consumer. Its runtime slots are `container:openbao`,
`container:converge` (both widen and converge Jobs), and, for Cloud SQL storage,
`init:cloud-sql-proxy`. Every generated resource carries the explicit provider
owner; a complete release resolves and freezes all image slots before rendering.
The helper images can be declared with `kubectl_image` and
`cloud_sql_proxy_image`; defaults match the native renderer. This never adopts
unrelated images or changes the bootstrap source or authentication boundary.

## Clients authenticate with JWT, and only JWT

The component converges one JWT auth mount per cluster (`jwt-<cluster id>`) and a
login role per principal: `consumer-<name>` for each entry of `consumers`, the
converge role, the writer, and one per environment a control plane declares. A
client logs in with a projected service-account token whose audience is the
store's (`audience` in the access document), verified against the cluster's
issuer.

It does **not** render Kubernetes auth or AppRole. The server pods mount no
service-account token, and the `system:auth-delegator` binding a Kubernetes-auth
login needs is not rendered.

## Adopting a live store: ship the clients' JWT flips in the SAME release

Replacing a hand-run OpenBao with the component keeps its name, namespace and
storage. What changes is how clients get in. **At the cutover every client still
on Kubernetes or AppRole auth loses access**:

- a token it already holds works until its TTL (about 15 minutes), then every new
  login is refused (`403`);
- a client with no cached token is locked out at once.

And every release restarts every workload (`service.version` rides the pod
template). So a flip shipped in a LATER release costs another full roll, and the
client stays down for the whole gap between the two. The SC7 prod adoption did it
one flip per release: the controller's tenant reads failed for about 14 minutes
and the identity reconciler's sweeps for about 27.

**The rule: one release carries the adoption AND every consumer's flip.**

Why that is safe, by construction: the secret ordering is

```
CRDs -> ESO -> OpenBao (widen, rollout, converge) -> stores -> ExternalSecrets -> workloads
```

The component's `widen` and `converge` Jobs run, and are awaited, BEFORE any
workload of the release is applied. So when a flipped client's new pod starts, its
JWT mount and its role already exist. Nothing has to be triggered by hand.

For each consumer, in the same release:

1. Declare it in `openbao.consumers` with exact grants (its role is created by the
   `converge` Job).
2. Point its login at the JWT mount and role, with the store's audience.
3. Mount the audience-bound projected token and the CA ConfigMap
   (`tls.ca_configmap`, published into the namespace by the converge Job) into its
   pod, and make the client read the token file on every login.
4. Remove the old Kubernetes-auth or AppRole configuration, so the client cannot
   fall back to a path that no longer exists.

A client you cannot flip in this release stays down until you do. Say which ones
before you start, and decide whether that is acceptable.

### The first converge needs a break-glass token

The self mount and the `openbao-converge` role do not exist in a store that was
never converged by the component, so the first converge cannot log in by JWT. It
needs one root token minted from the recovery keys
(`OPENBAO_BREAK_GLASS_TOKEN`), supplied for that run; the script revokes it at the
end. After it, JWT works and any credential only the old setup used can go.

### What forge tells you

`forge env deploy <env>` raises an advisory before it applies anything (the
`store_adoption` preflight finding, never blocking) when the component is about to
replace a Deployment or StatefulSet of the same name that forge did not install
(no `forge.dev/component-owner` label). It repeats the rule above. A store forge
already owns, and a `--target` deploy that does not install the store, do not
raise it.

### Retiring the previous store's auth mounts

The component converges only the `jwt-*` mounts it writes (its policy grants
`sys/auth/jwt-*`, and narrowing diffs only what an earlier converge wrote). A
`kubernetes` or `approle` mount the hand-run install left behind therefore stays
enabled after adoption, with every role in it still able to log in. Remove it
declaratively with `retired_auth_mounts`, once the audit log shows nothing has
used it:

```python
openbao = forge.OpenBao {
    retired_auth_mounts = [
        forge.OpenBaoRetiredAuthMount {
            name = "kubernetes"
            reason = "no login since 2026-10-25 11:41Z (audit log)"
        }
        forge.OpenBaoRetiredAuthMount {
            name = "approle"
            reason = "no login since 2026-10-25 12:27Z (audit log)"
        }
    ]
}
```

What the `converge` Job does with each entry, after it has applied and narrowed
the access document (the `widen` Job never retires anything):

- **Absent mount: skipped.** Idempotent, so the entry can stay in the file.
- **Present, no roles: disabled**, with the `reason` in the Job's log.
- **Present, roles remain: refused.** Exit 64, a verdict the Job does not retry,
  naming the roles. Disabling a mount revokes every token it issued and deletes
  its roles. Delete the roles first, or set `force = True` on the entry once they
  are known to be dead (it warns on every render and logs `FORCED`).
- **A mount type the Job cannot check** (anything but `kubernetes`, `approle`,
  `jwt`, `oidc`, whose logins are not roles) is refused the same way; `force`
  is the only way past it.
- **All entries are verified before any is disabled**, so a refusal never leaves
  the store half-retired.

The converge policy gains `delete` + `sudo` on exactly `sys/auth/<name>` and
`list` on `auth/<name>/role` for each name listed: never a wildcard, and never
on `access-admin`, which delegated principals hold. `reason` is required.
Refused at render: a name that is not a plain mount name (`jwt-*`, `a/b`), the
built-in `token` mount, a duplicate, and the `jwt-<cluster>` mount the component
manages (it would be enabled and disabled on every run; drop a cluster's mount by
no longer registering the cluster).

### Other leftovers

The previous store's PodDisruptionBudget, ingress NetworkPolicy, RBAC and
auth-delegator ClusterRoleBinding are pruned by the owned-object prune once the
rollout is healthy, if forge stamped them (`deploy/plan`). Objects applied before
the stamp existed carry no label: label them once, or delete them by hand. Two
PodDisruptionBudgets over one set of pods block every node drain, so check.

### Measured cost of each step (rehearsal)

| Step | Cost |
|---|---|
| store gains a plaintext transitional port, then clients move to it (two deploys) | 0 failed requests |
| clients moved to the new port in the SAME deploy that adds it | 95 s, every client |
| the adoption itself | about 6 s per client (a standby's `307` to the TLS address) |
| Kubernetes-auth clients, until flipped | every new login `403` |
| a flip in the adoption release | 0 |
| a flip in a later release | the whole gap |

## Transit: signing keys the store never releases

```python
openbao = forge.OpenBao {
    transit = [forge.OpenBaoTransit {mount = "transit"}]
    consumers = [forge.OpenBaoConsumer {name = "signer", workload = "signer", grants = [
        forge.OpenBaoGrant {path = "transit/keys/+", capabilities = ["create", "update", "read"],
            wildcard_read_reason = "public keys only: transit returns no private material"}
        forge.OpenBaoGrant {path = "transit/sign/+", capabilities = ["update"]}
    ]}]
}
```

The converge Job ENABLES each `transit` mount, after it writes the policies, and
never disables one (that would destroy every key on it). It holds
`sys/mounts/<mount>` and nothing under the mount, so it never creates, reads or
uses a key. Keys are the consumers' business: grant `<mount>/keys/+` to create a
key on first use and read its public half, `<mount>/sign/+` to sign. Use `+`,
not `*`: `*` is a prefix glob and would also grant `keys/<name>/config` (deletion,
export) and `keys/<name>/rotate`. Rotate deliberately, as an operator.

A store that declares no transit renders exactly as before. Reliant's control
plane uses one mount for per-org delivery-bundle signing keys
(`deploy/delivery-policy`, `signing`).

## HA: replicas never share a node

With `replicas >= 2` the component renders a hard topology spread
(`kubernetes.io/hostname`, `maxSkew: 1`, `whenUnsatisfiable: DoNotSchedule`), so a
node loss cannot take a Raft quorum, or the active node and its standby, with it.
A single-node cluster still schedules (one domain has no skew). Zone spread
(`topology.kubernetes.io/zone`) is best effort (`ScheduleAnyway`). A preference
was not enough: both prod pods landed on one node.

Relax it only with a reason, which warns on every render:

```python
openbao = forge.OpenBao {
    ha = forge.OpenBaoHA {
        node_spread = "preferred"
        node_spread_reason = "two-node staging pool; replicas share a node during upgrades"
    }
}
```

`node_spread_reason` without `"preferred"`, and `"preferred"` without a reason, are
both refused. A cluster with fewer schedulable nodes than `replicas` leaves the
surplus pods Pending under the default: size the pool, or relax it on purpose.

## Converge retries

The converge Job retries a connection-level failure (no HTTP answer: a node drain,
a pod restart behind the Service, a proxy hiccup) with bounded backoff, and fails
fast on a real API error, quoting the store's actual answer. It never replays
`sys/init` or a mount enable after an ambiguous failure. A refused credential is a
verdict (exit 64); an unreachable store is not, and is retried by the Job.
