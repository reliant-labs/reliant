---
name: deploy/external-secrets
description: The External Secrets Operator as a forge component (forge.ExternalSecretsOperator) — one hardened, cluster-wide install per cluster, what it renders, the three admission policies, how to bump the pin and verify the image, and the residual risk you accept by running it. Use when you declare the operator, when a SecretStore or ExternalSecret is refused at admission, or when you review what ESO can reach.
---

# External Secrets Operator

```python
external_secrets_operators = [
    forge.ExternalSecretsOperator {cluster = _hub}            # a ClusterTarget (GKE) or a k3d Cluster
    forge.ExternalSecretsOperator {cluster = _daemon_target}  # one per cluster, never two
]
```

Unset renders nothing. A declaration lowers to ONE `forge.HelmChart`
(`external-secrets`, apply it alone with `forge env deploy <env> --target
external-secrets`) bound to the cluster you named, plus the
`external_secrets_operators` record the release shape reads. A
`forge.ClusterTarget` is its own declaration: it needs no entry in
`Bundle.clusters`, which is how the chart reaches a cluster forge does not
create.

## What is installed, and what is not

| Control | Setting |
|---|---|
| Unused kinds | `ClusterSecretStore`, `ClusterExternalSecret`, `PushSecret`, `ClusterPushSecret`, `ClusterGenerator` and every generator have **no CRD** and are not processed. Only `ExternalSecret` and `SecretStore` can drive it. |
| CRDs | forge supplies three (ExternalSecret, SecretStore, GeneratorState) from `pkg/deploy/eso/crds/<version>/`, pinned to the chart. The chart renders with `installCRDs: false`: in chart 2.13 the CRDs are templates gated on `installCRDs`, so `--skip-crds` alone would still emit all twenty. |
| Webhook | `webhook.create` and `certController.create` are off. See P1. |
| Token creation | `rbac.serviceAccountTokenCreate: false`. The operator mints a token for no ServiceAccount until a Role with `resourceNames` names it (rendered per SecretStore namespace by the SecretStore component). |
| Aggregation | `rbac.aggregateToView/Edit/Admin: false`, `rbac.servicebindings.create: false`: a namespace admin does not inherit access to ESO kinds. |
| Pod | namespace Pod Security `restricted` (enforced); non-root, read-only root filesystem, seccomp `RuntimeDefault`, no privilege escalation, all capabilities dropped; 2 replicas, leader election, PDB, spread across nodes, kept off `reliant.dev/runtime=kata` nodes. |
| Supply chain | image pinned by digest (`ESO_IMAGE`), the chart by version. See P2. |
| Caching | `--enable-secrets-caching=false`, `--enable-configmaps-caching=false`, `--enable-managed-secrets-caching=true`: the informer holds only Secrets an ExternalSecret manages. |
| Network | a NetworkPolicy: DNS, and egress on `admission.egress_ports` (443, 6443, 8200) to `admission.egress_cidrs`; ingress only metrics from `admission.scrape_namespaces`. Narrow `egress_cidrs` to your API server and store. |
| Ownership | every object carries `forge.dev/component-owner=<project>.<env>`. A second env declaring the operator on a cluster whose CRD another owner holds is refused before any write (`ComponentOwnerError`). A CRD with no owner label is adopted. |

`values` is the escape hatch: a raw overlay merged **deeply** over the posture
(maps merge, lists replace, last wins). `extraObjects` is appended, so adding an
object never costs the admission policies. Overriding a hardening value is the
20% path; nothing refuses it.

## P1 — does chart 2.13 keep CRD conversion off with the webhook disabled?

**Yes.** `crds.conversion.enabled` defaults to `false` and is the only thing
that makes a CRD carry `spec.conversion` (it adds the cert-manager injector
annotation; no template in the chart emits a `conversion:` stanza at all).
Rendered with `crds.conversion.enabled=true` and `webhook.create=false` the CRDs
still have no `spec.conversion`, and the CRDs forge embeds serve only `v1` (the
deprecated `v1beta1` is `served: false`). The API server therefore never calls a
webhook that is not there; `webhook.create=false` is the chart's own documented
pairing with conversion off. `TestCRDsBundle` pins both facts. The validating
webhook (`secretstore-validate`, which only checks a store's provider config) is
replaced by the admission policies below, evaluated in-process by the API server.

The one real dependency found while checking: the controller starts a
GeneratorState reconciler unconditionally (`--enable-generator-state` defaults
true), and without the `generatorstates` CRD it exits at start-up with "no
matches for kind GeneratorState". That CRD is therefore in the bundle (and the
controller keeps its read of the generators API); no generator kind is
creatable, because their CRDs are absent.

## P2 — the cosign identity

The controller image is signed keyless by GitHub Actions. For the pinned digest:

```bash
cosign verify \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity https://github.com/external-secrets/external-secrets/.github/workflows/release.yml@refs/heads/main \
  ghcr.io/external-secrets/external-secrets@sha256:<digest>
```

Checked against `v2.13.0@sha256:f799d87a…`: the certificate's SAN is that
workflow URI and its issuer extension is the GitHub OIDC provider; the
signature, the Rekor entry and the chain verify. SLSA provenance and the SBOM
verify with `cosign verify-attestation --type slsaprovenance` / `--type spdx`
and `--certificate-identity-regexp '^https://github.com/external-secrets/external-secrets/'`.
The chart (`oci://ghcr.io/external-secrets/charts/external-secrets`) is signed
under the same identity. forge holds the issuer and identity as
`eso.CosignIssuer` / `eso.CosignIdentity` (`pkg/deploy/eso`); **forge has no
cosign hook yet**, so verification is a step of the pin bump below, not of a
deploy.

## Bumping the pin

1. `helm show chart oci://ghcr.io/external-secrets/charts/external-secrets --version <v>`; note the appVersion.
2. `crane digest ghcr.io/external-secrets/external-secrets:v<app>` and run the `cosign verify` above on that digest. Stop on any mismatch of issuer or identity.
3. Regenerate the CRDs: for each of `externalsecret`, `secretstore`, `generatorstate`,
   `helm template x <chart> --version <v> -n external-secrets --set installCRDs=true --show-only templates/crds/<name>.yaml | tail -n +3 > pkg/deploy/eso/crds/<v>/<plural>.yaml`. Check the diff for a new `spec.conversion`.
4. Update `ESO_CHART_VERSION` and `ESO_IMAGE` in `kcl/schema.k`, and `ChartVersion` / `ImageTag` in `pkg/deploy/eso`. `TestKCLPinsAgree` and `TestBundleCarriesNoDriftMarkers` fail until they agree.
5. `task test -- ./internal/cluster/ ./pkg/deploy/eso/` (renders the real chart; the RBAC invariants must still hold).

## The admission policies

`ValidatingAdmissionPolicy` objects evaluated by the API server itself, so there
is no webhook to be down. All fail closed (`failurePolicy: Fail`, action `Deny`);
each refusal names the field to extend. An update that leaves `spec` alone
(finalizers, annotations) passes the two input policies, so tightening a policy
never wedges a deletion.

| Policy | Refuses |
|---|---|
| `eso-writers` | a create, update or delete of an ExternalSecret or SecretStore by anyone but `admission.writers`, `admission.writer_groups` (default `system:masters`), the operator, and the cluster's garbage collector and namespace controller. Name your apply identity: a GKE binding's Google service account email, or the `forge-connect-<cluster>-<env>` ServiceAccount `forge cluster connect` mints. |
| `eso-store-pin` | any SecretStore except a `vault` provider that dials a server in `admission.servers` with JWT auth on a mount in `jwt_mounts`, with a projected token for `admission.store_service_account` (default `reliant-secrets`, in the store's own namespace) and exactly one audience from `admission.audiences`; or a `gcpsm` provider over Workload Identity for a project in `gcp_projects`. A `webhook` provider is refused: it is an exfiltration channel. |
| `eso-es-policy` | `dataFrom`; a `ClusterSecretStore` reference; a `creationPolicy` other than Orphan or Merge (including the default, Owner); a `deletionPolicy` other than Retain; a `target.name` other than the ExternalSecret's own; a generic target; a per-entry `sourceRef`; a refresh under 5m. |

`TestESOAdmissionPolicies` runs all three against a real kube-apiserver with the
embedded CRDs (`KUBEBUILDER_ASSETS`, see `setup-envtest`); it skips without them.

An empty list is not a wildcard: policies built from `servers = []` would admit
no Vault store. So `servers`, `jwt_mounts`, `audiences` and `gcp_projects` that
you leave empty are DERIVED from the same render: each SecretStore the Bundle
emits on that cluster (its URL, its `jwt-<cluster id>` mount, its one audience,
a `gcpsm` store's project), plus the in-cluster `forge.OpenBao`'s own server,
mount and audience when it runs there, admitted even before any ExternalSecret
reads from it. A list you set replaces the derived one for that list only (the
others stay derived). A SecretStore this Bundle does not render, for example one
a project applies by hand, is not derived: name its server in `admission.*`.
`TestESODerivedAdmissionAdmitsTheBundlesOwnObjects` applies a Bundle's own
objects to a real kube-apiserver with no hand-filled `admission`.

## What the deploy preflight checks for store-backed Secrets

A `forge.ExternalSecret` with a `source = forge.StoreSource {...}` is created by
the operator **after** the deploy, so `forge env deploy` does not demand it up
front the way it demands an out-of-band Secret. In its place, before anything is
applied, it checks each cluster that holds one (`internal/cluster/store_preflight.go`):

| It checks | A refusal says |
|---|---|
| the `ExternalSecret` and `SecretStore` kinds are served, at `external-secrets.io/v1` (skipped when this deploy installs the operator: its CRDs apply first) | declare `external_secrets_operators` and deploy `--target external-secrets` first, or deploy the env that owns the cluster's operator |
| one owner per cluster: an operator labelled for another env is refused before any write (an unlabelled one, installed by hand, is adopted) | remove this env's entry and let the owner's bundle own the cluster |
| an in-cluster `forge.OpenBao` finished converging (its newest converge Job succeeded), unless this deploy applies it | deploy it first with `--target <name>`; the command to inspect the Job is in the message |
| the store holds a value for every name the ExternalSecrets read, less the names `source.generate` mints. Names and versions only, never a value | `forge secret set <NAME>`, `forge secret import`, or declare it under `source.generate` |

A store that cannot be asked (no credential, control plane unreachable) is a
**warning**, not a block. A `creationPolicy = "Merge"` Secret only updates one
that exists, so it keeps the ordinary existence check; Orphan (the default) does not.
A hub-reconciled env (`ControlPlane`) is not refused for a Secret the operator
delivers; for one nothing delivers, the refusal names `source = forge.StoreSource`
first, then `forge env secrets sync`. `--skip-preflight` bypasses all of it.

## Residual risk

**What remains.** The controller's ClusterRole grants `secrets
get/list/watch/create/update/patch/delete` cluster-wide. The chart has no switch
for this short of `scopedRBAC`, which needs one release per namespace and
fights over the cluster-scoped CRDs. A compromise of the operator process (a
provider-response parsing bug, a malicious image, a node escape onto its node)
can read and overwrite **every Secret on that cluster**, and can mint tokens for
every `reliant-secrets` ServiceAccount its Roles name.

**What bounds it.**

1. *What drives it.* Only ExternalSecret and SecretStore CRs; the cluster kinds,
   PushSecret and the generators have no CRD; writers are limited by
   `eso-writers`; inputs by `eso-store-pin` and `eso-es-policy`.
2. *What the store gives.* The store decides, not ESO: a role carries a
   namespace's exact paths, so a crafted ExternalSecret in namespace A gets 403
   for B's names. A compromised operator can fetch only names already granted to
   namespaces on its own cluster, which already exist there as Secrets. The store
   adds no exposure beyond what the cluster holds.
3. *Process hardening* (the table above) narrows the paths to compromise: no
   exec shell (the image is distroless), read-only filesystem, restricted pod,
   a NetworkPolicy bounding where stolen values can be sent, no webhook endpoint
   to attack.
4. *Detection.* With managed-secrets caching, the operator's steady state lists
   only labelled Secrets. Alert on any `get`/`list` of Secrets by its
   ServiceAccount in a namespace with no ExternalSecret, or of a Secret without
   the managed label (GKE audit logs; Data Access for core/secrets).

**Not enforced by forge, and yours to hold.** The invariant *no subject other
than break-glass holds `pods/exec` in the ESO namespace* is a property of your
cluster's RBAC, not of this chart: forge renders no grant of `pods/exec`
(`TestESOChart_RBACInvariants` asserts the chart has none, nor any aggregation,
impersonate/escalate/bind verb or token-create), but a cluster-admin or a broad
ClusterRole you bind elsewhere reaches the pod anyway. Audit who can exec into
`external-secrets`.

**A listed exception, not a new kind of risk.** Workload controllers elsewhere
(a workspace controller, a Flux applier) already hold cluster-wide Secret grants.
The operator is one more holder, with narrower inputs than any of them; record it
beside them as an exception to "no cluster-wide Secret reader".

## When a refusal blocks you

The message names the policy and the field. Extend `admission.*` and redeploy
with `--target external-secrets`; do not delete the policy. A SecretStore that
predates the policies keeps working and can still be deleted; a change to its
`spec` is judged. To stop forge managing a cluster's operator, remove the
declaration — the objects stay (a CRD is never pruned) until you delete them.
