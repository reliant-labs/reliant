---
name: deploy/openbao-bootstrap
description: How forge.OpenBao gets the values it needs before it can serve a read — the seal key and the database password delivered from Google Secret Manager, and the `operator init` output escrowed back to it. Covers `Bundle.bootstrap_source`, `BootstrapSecret.generate`, the two Google service accounts and what each may do, the recovery escrow sink, the escrow warning, and the exact Google-side setup. Use when you declare a self-run OpenBao outside a local env, when a deploy stops at the escrow, or when you provision the Google side.
---

# OpenBao bootstrap and escrow

This is the bootstrap half. For the component, its client auth and the order to
adopt a live store in, load `deploy/openbao`.

A secret store cannot hold the values it needs to start. Three things must
exist before OpenBao can serve a read, and none can live inside it:

| | Value | Why it is outside |
|---|---|---|
| B1 | the static **seal key** | circular otherwise; it is also what makes the store's backups readable |
| B2 | the **Postgres password** (Postgres storage only) | storage dials it at boot |
| B4 | the **`operator init` output** (recovery keys, initial root token) | break-glass; must survive the loss of the namespace |

`Bundle.bootstrap_source` says where they live.

```python
bootstrap_source = forge.GCPSecretManager {project = "acme-prod"}   # deliver B1/B2, escrow B4
bootstrap_source = forge.OutOfBand {}                               # you provision them, and the render only checks them (the default)
```

## `forge.GCPSecretManager`

forge renders, in the store's namespace:

- a SecretStore (`<name>-bootstrap`) over Secret Manager, logging in with
  **Workload Identity** as the service account `<name>-bootstrap-reader`; no key
  is stored anywhere;
- one ExternalSecret per value: `<name>-unseal` (the seal key, read at **version
  1, forever**, so an extra version can never change what the store opens with)
  and, for Postgres, `<name>-storage` (the DSN, composed around the password with
  `urlquery`). Both are `creationPolicy: Orphan`, `deletionPolicy: Retain`: a
  Secret Manager outage, or a Flux prune of these objects, never removes the
  Secret a running store reads;
- a Role letting ESO mint a token for **that service account and no other**;
- the converge Job's escrow hook (below).

No prerequisite Secret is declared: the deploy preflight does not block on
`<name>-unseal` / `<name>-storage`, because the operator creates them.

### The two Google identities

Each is federated to ONE Kubernetes service account in the store's namespace and
holds one narrow role. They are different accounts on purpose.

| Google service account | Kubernetes service account | Role |
|---|---|---|
| `reader_gsa` (default `<name>-bootstrap-reader`) | `reader_service_account` (default `<name>-bootstrap-reader`) | `roles/secretmanager.secretAccessor` on **B1 and B2 only** |
| `escrow_gsa` (default `<name>-escrow-writer`) | `<name>-converge` (the converge Job's own) | `roles/secretmanager.secretVersionAdder` on **B4 only** |

`secretVersionAdder` is `secretmanager.versions.add` and nothing else: the Job
cannot read the escrow, list its versions or delete anything, even with a stolen
token. A bare account id gets `@<project>.iam.gserviceaccount.com`; a full email
is used as given.

```python
bootstrap_source = forge.GCPSecretManager {
    project = "acme-prod"
    reader_service_account = "openbao-bootstrap-reader"   # the default for a store named "openbao"
    reader_gsa = "openbao-bootstrap-reader"
    escrow_gsa = "openbao-escrow-writer"
    # cluster_project / cluster_location / cluster_name: read from a
    # `gke_<project>_<location>_<cluster>` kubectl context; set them for any other.
}
```

**The Google side is provisioned outside the Bundle**, never by a deploy: the
secret containers (with no version), the two accounts, their Workload Identity
bindings (`<namespace>/<ksa>`) and the IAM above. forge renders the Kubernetes
half and the annotations that name the Google accounts; the trust itself is
yours. The ESO policy `eso-store-pin` admits the store because the project is
in `admission.gcp_projects` (derived from the render).

### `BootstrapSecret.generate` — minted at deploy, once

```python
seal = forge.StaticSeal {key = forge.BootstrapSecret {
    name = "openbao-unseal-prod"
    generate = forge.GeneratedSecret {bytes = 32, encoding = "hex"}      # the default
}}
storage = forge.OpenBaoPostgres {password = forge.BootstrapSecret {
    name = "openbao-pg-password-prod"
    generate = forge.GeneratedSecret {kind = "password", length = 32}     # opt in
}}
```

`forge env deploy` creates a generated bootstrap value **if its container holds no
usable version**, with the deployer's own Application Default Credentials, before
ESO is asked to deliver it. It never overwrites, rotates or deletes:

- a container with a usable version is left alone, whatever it holds;
- a container with only a disabled or destroyed version is an **error**: forge
  will not mint over a value somebody took out of service;
- a container that does not exist is an **error**: the identity that reads it is
  granted on the container, so forge does not create one;
- Secret Manager has no check-and-set. Two deployers racing can both add. The one
  whose version is not 1 **disables the version it added**, so version 1 and
  "latest" stay one value. A deployer that holds `versions.add` but not
  `versions.disable` fails with the exact `gcloud secrets versions disable`
  command to run.

The recovery escrow refuses `generate`: it is written by the converge Job.
`--dry-run` reports what would be minted and writes nothing. No command prints a
value.

An external value (a password you chose) omits `generate` and is provided with
`forge secret import --env <env> --to-bootstrap <NAME> ...` (create-only).

### The recovery escrow

The converge Job runs the **escrow hook** after `operator init` and after the
in-cluster recovery Secret is written, and **before it converges anything**. If
the hook fails the run fails: a store whose recovery output is not escrowed
outside the cluster is not converged.

The identity holds `versions.add` only, so it cannot read the container to learn
whether it is empty. "Copy if empty" is decided in the cluster: a ledger
ConfigMap (`<name>-escrow-ledger`) records the UID of the recovery Secret that was
escrowed. The recovery Secret is created once and never rewritten, so its UID
names one `operator init` output.

| State | What the hook does |
|---|---|
| ledger holds this UID | nothing; no Google call |
| anything else (first run; an already-initialised store whose escrow is empty; a lost ledger) | add **one** version, then write the ledger |

A crash between the add and the ledger write adds a second, identical version on
the next run. That is the whole cost of the sink being unable to read.

`forge env deploy` of a store whose escrow refused tells you which role is
missing (`roles/secretmanager.secretVersionAdder` on that one secret) and the
next run carries on from the same initialised store: it copies the existing
recovery output rather than re-initialising.

The Job's NetworkPolicy gains egress to the GKE metadata server
(`169.254.169.252:988`, and `169.254.169.254:80` on older nodes) when a bootstrap
source is declared; without it a default-deny policy leaves the Job unable to
get a token.

## `forge.OutOfBand` and the escrow warning

Under `OutOfBand` (the default), `<name>-unseal` and, for Postgres,
`<name>-storage` are **prerequisite Secrets** the deploy preflight blocks on, and
nothing escrows the recovery output. A real env (no `lifecycle`) in that state
keeps the seal key and recovery output inside the cluster they protect, and every
render says so (`openbao.warnings`; `forge lint --secrets` reports the same with a
file position). It is a **warning**, not a refusal: an escrow you run yourself is
legitimate. Silence it by declaring `GCPSecretManager`; a local or ephemeral env
never warns.

## Prod: what to set

For a hub on `gke_<project>_<location>_<cluster>` running OpenBao on Cloud SQL, the
whole declaration:

```python
openbao = forge.OpenBao {
    namespace = "control-plane-prod"
    storage = forge.OpenBaoPostgres {
        cloudsql_instance = "<project>:<region>:<instance>"
        gcp_service_account = "<openbao GSA email>"
        password = forge.BootstrapSecret {name = "openbao-pg-password-prod"}
    }
    seal = forge.StaticSeal {key_id = "prod-1", key = forge.BootstrapSecret {name = "openbao-unseal-prod"}}
    recovery = forge.OpenBaoRecovery {escrow = forge.BootstrapSecret {name = "openbao-recovery-prod"}}
}
bootstrap_source = forge.GCPSecretManager {
    project = "<project>"
    reader_service_account = "openbao-bootstrap-reader"
}
```

The Kubernetes service accounts the Google side must trust are therefore
`<openbao namespace>/openbao-bootstrap-reader` and
`<openbao namespace>/openbao-converge`. If the store is not named `openbao`, the
defaults follow the name; set the fields explicitly to keep an existing binding.

## Failure modes

| Symptom | Cause | Fix |
|---|---|---|
| `escrow hook failed ... Secret Manager refused` | `escrow_gsa` lacks `secretVersionAdder` on the escrow container | grant exactly that role on that one secret; rerun |
| `cannot get a Google access token ... metadata server` | the converge service account is not bound to `escrow_gsa`, or the node pool has no GKE metadata server | add the Workload Identity binding for `<ns>/<name>-converge` |
| `projects/.../secrets/... does not exist: create the container first` | the escrow container was never created | create it empty |
| `<name>-unseal` never appears | the reader is not bound, or `reader_gsa` lacks `secretAccessor` on the seal key | check the binding for `<ns>/<reader_service_account>` and the grant |
| `bootstrap secret ... disabled or destroyed` | the container's only version was taken out of service | enable it, or import a replacement |
| a deploy fails with `no Google Application Default Credentials` | the deployer cannot mint | `gcloud auth application-default login`, or provide the values with `forge secret import --to-bootstrap` and drop `generate` |
