---
name: deploy/hosting
description: The scaffolded staging/prod envs are hosted on Reliant — what `_hosted` / `_hosted_frontend` wire, why the API is one workload (`_api`, the binary's `server`) so the browser reaches every service at one origin, rebinding to a cluster you operate or a frontend to your own bucket, kinds hosting refuses (cron, operator), and how an existing cluster-bound project adopts hosting.
---

# Hosted by default

`forge project new` scaffolds `staging` and `prod` hosted on the forge control
plane (Reliant cloud). `dev` stays local (`forge.OnHost` + `forge.HostInfra`).

| In the env | What it is |
|---|---|
| `control_plane = forge.ControlPlane {}` | Reliant cloud; set `endpoint` for another control plane |
| `_db = forge.ManagedDatabase {runtime = forge.OnHosted {}}` | a dedicated Postgres, `deletionPolicy` RETAIN |
| `_hosted(_api)` | THE API: the binary's `server` — every service and worker — as one workload |
| `_hosted(wl.<name>)` | a workload `server` does not run (a job such as `migrate`; one you split out) |
| `_hosted_frontend(_web_frontend)` | the static export on platform static hosting: the platform owns the bucket, the CDN and the hostname |
| `secret_provider = forge.HostedSecrets {}` | write-only store: `forge secret set --env <env> <KEY>`; who may use a secret: `forge secret allow` (below) |

`_hosted` also:

- sets `image` to the declared image minus its registry host — the platform
  pulls only from its own registry, and forge composes
  `<registry>/<org>/<project>/<name>` from your credential (`deploy/hosted-registry`);
- sets `DATABASE_URL = forge.DatabaseRef {name = _db.name}` on every workload
  listing it in `config_secrets`;
- sets a service's `CORS_ORIGINS = forge.WorkloadURL {workload = "<site>"}`
  and `CORS_ALLOW_CREDENTIALS = "true"` when the project was born with a
  frontend (`forge scaffold frontend` later prints the line to add). They
  serve a browser that calls the API's own host (a literal `API_URL`, below):
  there the site and the API are different origins, and without credentials
  allowed `/auth/login` answers 200 and every call after it 401s.

`_hosted_frontend` sets the bare `image` and `runtime_config = _api_config`
(`{API_URL = forge.WorkloadURL {workload = "api"}}` once `_api` is bound): the
control plane writes config.js after every sync, so one release promotes
unchanged.

**The browser reaches the API on the site's own origin.** An `API_URL` that
references a workload is written into config.js as `"/_api"`, and the platform
serves that workload at `/_api/*` on every host of the site — its platform
name and every custom domain — stripping the prefix (`/_api/acme.v1.Svc/Get`
reaches the API as `/acme.v1.Svc/Get`). It is the path `forge env up` proxies in
dev, so the bundle behaves the same in both. Native sign-in's host-only
`SameSite=Lax` cookie is therefore first-party to the site, whatever the
Public Suffix List says about the two hostnames, and no CORS is involved.

- The API sees a direct call: `X-Forwarded-Host` is its own hostname, so
  redirect URIs and absolute URLs it builds stay on that host, which keeps
  serving CLIs, mobile apps and servers.
- A `Set-Cookie` scoped to the API's host becomes host-only on the site; a
  `Path` below `/` moves under `/_api`. API responses are never cached by the
  platform's CDN (`Cache-Control` becomes `private`).
- `/_api` is reserved on hosted sites: with no API it answers 404, not the
  SPA fallback.
- To have the browser call the API's own host instead, set the URL as a
  literal (`API_URL = "https://<api host>"`): a literal is never rewritten,
  and the CORS settings above make it work while the two hosts are same-site.

The frontend must be a static export (`output: static`, which the
scaffold writes). A frontend scaffolded by forge ≤ v0.1.43 has no `output:`
(it is standalone) and dynamic `[id]` pages: `forge skill load
migrations/v0.1.44`.

## Shipping it: `forge env deploy prod`

Signed in to Reliant (`reliant forge …`, or a Reliant agent's shell) there is no
`forge login`: forge mints a token from your session. Standalone forge uses
`forge login`; CI sets the env's token variable. Deploy `staging` first, then
promote the same release to `prod`; check with `forge env status <env>`.

To make that order binding, declare it in `deploy/kcl/prod/main.k`:
`delivery_policy = forge.DeliveryPolicy {health_gate = forge.HealthGate {after
= ["staging"]}}`. The control plane then refuses to promote a release to prod
until its own release-health verdict for the release on staging is PASS
(`health_gate_pending` exit 4 while the window is open: `forge obs check
staging --release <v> --wait`; `health_gate_regressed` exit 1 on a FAIL;
`health_gate_unproven` exit 4 otherwise). `--override-health-gate "<why>"` is
the recorded incident override. A new project declares no order, so its first
`forge env deploy prod` is never gated. See `deploy/delivery-policy`.

### No account: `--claimable`

An agent with no Reliant account (standalone forge, nothing signed in) can still
publish:

```bash
forge env deploy prod --claimable
```

forge gets an anonymous identity from the control plane, deploys as it, and
exits **0** with the live URL and a `claim` block: the claim link, when the
deploy goes offline (72h; then deleted 7 days later) and an instruction for you.
**Give the claim link to your user and tell them it expires.** Opening it signs
them up for free and makes the deploy theirs, with nothing to copy. Treat the
link like a password (whoever claims first owns the deploy) and never post it
publicly. `forge env status <env> --claimable` prints it again; `--json` carries
it as `claim` (`state`, `url`, `claim_url`, `expires_at`, `purge_after`,
`agent_instructions`).

| Rule | Detail |
|---|---|
| Only with the flag | There is no auto-detection. With no credential, a plain `forge env deploy` fails and its error names `--claimable` |
| Signed in? Refused (exit 1) | A user with a login or `$FORGE_CREDENTIAL_HELPER` deploys to their own org: drop the flag |
| Fits | one static site, or one service (1 replica, at most 125m / 128 MiB, request and limit, no burst) with one managed database (256 MiB of data on a 1 GiB volume) and that service's migration job (a gating `before = [fw.BEFORE_ALL]` job, or a standalone pre-rollout one). No workers, cron, second service or database, custom domains or remote builds: exit 4, naming each with its KCL fix |
| **No outbound internet until claimed** | An unclaimed **service** (not a static site) serves requests and uses its own database, but every call out of it fails: a payment, an LLM, a webhook, a package download, and any DNS lookup of a name outside the cluster (the connection errors, or the name does not resolve: the backend's resolver answers only the cluster's own names, such as its database's). That is the platform, not a bug in the app, so do not debug it or work around it; it ends when the deploy is claimed, when the platform restarts the backend on the normal hosted network (TCP 80/443 to the public internet). Plan for it: an app that needs an external API at startup will not come up unclaimed. An LLM call goes through Reliant's gateway instead of the internet: `deploy/llm` says what an unclaimed deploy gets today. The claim block, `--json` (`claim.no_outbound_internet`, and `claim.agent_instructions`) and `--plan-only` all say so |
| Not the shape band | The billed plan's 4 GiB-per-vCPU band does not apply to an anonymous deploy (it is never billed); the caps above do. `forge env shape <env> --claimable` judges an env that way |
| Preview first | `forge env deploy <env> --claimable --plan-only` checks everything locally (the caps, the hosted plan, the resources) and asks the platform whether it takes anonymous deploys right now. No identity, nothing created; exit 0 when the env fits |
| Scaffold for it | `forge project new <name> --claimable ...` writes a prod that fits: `control_plane = forge.ControlPlane {claimable = True}` (every check then judges it under this plan, and a signed-in deploy is refused until you remove the line) and `_claimable_resources` on its hosted services and jobs |
| Unavailable: exit 4 `claimable_unavailable` | The platform cannot take it: `disabled by operator`, an `unavailable dependency (<component>)`, `not supported yet` (a cause the platform reserves for a capability it lacks), or `not deployed` (no ClaimableService). Checked before any build; `--json` carries `unavailable_cause` and `unavailable_component`. Not your project: retry later, or deploy to your own account |
| Never queued | An unclaimed deploy is refused (exit 4), never held for billing; **exit 7 does not occur** |
| Throttled: exit 4 `claimable_throttled` | Our rate limits or the platform's anonymous sign-up limit. The block prints a scope (`network`, `device`, `platform`), a retry-after and the `forge login` alternative; `--json` carries `retry_after_seconds`, `retry_at`, `throttle_scope`. **forge never retries on its own, and neither should you in a loop**: wait, or `forge login` |
| Claimed: exit 4 `claimable_claimed` | A human claimed it. Deploy as them: `forge login`, then `forge env deploy <env>` without the flag |
| Redeploys | Re-running reuses this machine's deploy and its claim link and does not extend the expiry |

The anonymous token is stored beside your logins under its own credentials-file
client id (`forge-cli-claimable/<project>/<env>`), so it can never pass for a
`forge login` and `forge logout` leaves it alone. It carries only
`deploy:read` and `deploy:write`.

### Gates: what holds a deploy, and how each clears

| Gate | What happens | Clear it |
|---|---|---|
| **Billing** — a hosted workload, managed database or static site, and the org has no Reliant Compute plan | The deploy is **queued, not refused**: built, recorded, promoted, held (rollout phase `HELD`). `env deploy` prints waiting-on/why/do/link and **exits 7**; `env status` also exits 7; `--json` carries the same facts | Someone who can manage billing for the org subscribes in Reliant → Settings → Billing (the printed link goes there). Nothing to re-run: the Stripe webhook releases it. Block with `forge env deploy --wait` or `env status <env> --wait`; then `forge env smoke <env>` |
| **Over the plan ceiling**, **plan unknown**, **spend cap reached** | Refused, not queued (a spend cap is deliberate: it can clear unattended) | Reduce resources, upgrade, retry, or raise the cap |

Exit 7 is a person's turn, not a failure: relay the link to the user, do not
retry in a loop. A newer deploy to the same env supersedes a queued one. An
older control plane (no holds) still refuses with the billing reason.

### What hosting provides and refuses

| Provides | Refuses (bind to a cluster you operate) |
|---|---|
| kinds `service`, `worker`, `job`; `replicas`, `resources`, `command`/`args`, `probes`, `storageGiB`, `activeDeadlineSeconds`, `strategy` | kind `cron` (a CronJob, not metered yet; a worker running its own scheduler IS fine) |
| `ports` (the `expose = True` one is routed) | `operator`, `namespacedRBAC`, `clusterRBAC`, `crds`, ServiceAccount fields: no Kubernetes API on shared nodes |
| env from a literal, `forge.ManagedSecret`, `forge.DatabaseRef`, `forge.WorkloadURL`; a config-projected `forge.SecretRef` lowers to a ManagedSecret | `sidecars`, `volumes`, `securityContext`, `terminationGracePeriodSeconds`, `podAnnotations`: the platform composes the pod |
| a managed Postgres (`forge.ManagedDatabase`) | `nodeSelector`, `tolerations`, `priorityClassName`: the platform decides placement |
| static frontends on hosted static hosting (static export only) | `ports.domains` (use `forge domain`); raw `secretRef`/`configMapRef`/`fieldRef` env; a hostless or unpinned image |

## The API is one workload

A browser reaches the API at ONE origin: the frontend's Connect transport has
one base URL (a call is `/<package>.<Service>/<Method>`, so one origin serves
every service), and sign-in answers with an HttpOnly session cookie the browser
returns to that origin only. The platform gives every workload its own hostname
and routes no paths between them. So a hosted env does not host each service
as its own workload — the frontend could call only one of them, and would be
signed in to none of the others — it declares `_api`:

```kcl
_api = fw.Workload {
    name = "api"
    kind = "service"
    image = "ghcr.io/acme/shop"
    build = forge.GoBuild {cmd = "./cmd/shop", output_name = "shop"}
    args = ["server"]        # every service on one Connect mux, every worker beside it
    ports = [fw.Port {name = "http", port = 8080, expose = True}]
    config_secrets = ["DATABASE_URL"]
}
_workloads = [
    _hosted(wl.migrate)
    _hosted(_api)
]
```

- workloads.k still declares each service and worker; **dev** runs the same
  `_api`, as a host process under air (hot reload).
- `forge scaffold service|worker` adds no line to a hosted env — `server`
  already runs it — and binds `_api` if it was not bound yet (a project born
  with no service declares `_api` unbound, so it pays for no idle workload).
  A job, an operator and a tool bind on their own line as before.
- `server` runs no operator on hosted: with no Kubernetes API it logs
  `operators disabled` and serves on. The operator runs `_on_cluster`.
- **Splitting one out** — a service no browser calls, a worker that needs its
  own capacity: bind it on its own line, `_hosted(wl.<name>)`, AND give it a
  root of its own that `server`'s root no longer holds (a `//forge:root`
  struct in internal/app; `architecture`, "Roots and commands"), or it runs
  twice. A split-out service the browser does call needs an
  origin the frontend can reach — which hosting does not give it.
- `forge env new <env> --from prod --bind api=cluster` moves the whole API;
  `--bind <service>=…` is refused there, naming `_api`.

## Secrets a Reliant workflow uses: access lists

A hosted secret is opaque and write-only, and it carries an **access list**: the
principals allowed to use it. A Reliant workflow uses a secret by declaring it
(top-level `secrets: {NAME: {environment: <project>/<env>}}`) and writing
`{{ secrets.NAME }}` in an `http/request@1` header; the control plane
substitutes the value when the request is sent, so the value never reaches the
workflow, its logs or the model. It does so only if the workflow is on the
list. The list names no destination, path or step: it answers "may this workflow
use this secret", and what the credential can do is the receiving app's call.

```bash
forge secret set    --env staging TRADER_KEY                     # the value, on stdin
forge secret allow  --env staging --workflow trader-bot --project <reliant project id> TRADER_KEY
forge secret access --env staging [TRADER_KEY] [--json]          # who may use it
forge secret deny   --env staging --workflow trader-bot --project <reliant project id> TRADER_KEY
forge secret list   --env staging                                # each secret's access beside its version
```

- `--workflow` is the workflow's name (its `.reliant/workflows/<name>.yaml`);
  any run or trigger of it is covered. `--project` is the Reliant project id
  that owns it: find it with `reliant project list` (the row whose PATH is the
  checkout holding the workflow). forge never guesses it.
- `allow` and `deny` are idempotent. Only an org owner or admin edits a list
  (a machine token with no acting user is refused; a protected env asks for
  step-up). The control plane's refusal is printed as sent.
- A run denied for lack of access names this command in its error.
- Hosted envs only: a `forge.FileSecrets` store has no access list, and these
  commands say so. Workloads keep receiving secrets through `managedSecret` /
  `config_secrets`; access lists govern workflows.

## Hosting elsewhere

The cluster and bucket binders are declared beside the hosted ones, unused,
with what to fill in shown in a comment:

```kcl
_cluster = forge.ClusterTarget {          # was: _cluster = None
    cluster = "gke_acme_us-central1_prod" # its kubectl context
    connected_cluster = "acme-prod"       # forge cluster connect acme-prod --context <ctx> --env prod
    namespace = "acme-prod"
    platform = "amd64"
}
_workloads = [
    _hosted(wl.migrate)
    _on_cluster(_api)                     # rebound
]
```

The env still has a control plane, which deploys to the cluster too, so the
cluster is registered once (`forge cluster connect`) and named in
`connected_cluster`; the render refuses an unconnected one, naming the
command. An `_on_cluster` workload pulls the image workloads.k declares (push
it: `forge env build <env> --push`), gets the env's capacity floor, and reads
`DATABASE_URL` from the `<project>-secrets` Secret you provision in its
namespace (the managed database is reachable from the platform only). Moving
a whole env? Also add the commented `cluster_target`, `network_policy` and
gateway lines at the bottom of the Bundle; leaving hosting altogether, drop
`control_plane`, `_db`/`databases` and use `forge.ExternalSecrets {}` (then
no `connected_cluster`). A frontend: `_bucket = forge.OnBucket {bucket =
"..."}` and `_on_bucket(_web_frontend)`. Compose: `wl.<name> | {runtime =
forge.OnCompose {}}`.

A binding to `_on_cluster` / `_on_bucket` before its target is declared fails
the render, naming the workload and the fix. It is never a silent no-op.

## Kinds hosting refuses

An operator (no Kubernetes API on shared nodes) and a `kind = "cron"`
workload — a Kubernetes CronJob, not metered yet — cannot run hosted. forge's
own cron component (`forge scaffold worker <name> --kind cron`) is a worker
running its own scheduler, so it IS hosted.

`forge scaffold operator` binds the new workload `_on_cluster` in every
deployed env — the only binding that can run it — and warns, naming the envs,
that they refuse to render until `_cluster` is declared. (Its `crds = []` is
fine: an operator may own no CRD yet; `forge scaffold crd <Kind>` adds each
kind to that list, which is what its derived ClusterRole covers.) The ways
out:

1. declare `_cluster` — the env becomes mixed (hosted + your cluster);
2. drop the line from an env that should not run it (dev runs it on k3d).

The scaffold does not refuse the workload: its code is useful in dev, and a
render error at the binding is where the fix is made. Every other new
workload binds where the env's `migrate` job runs, so an env scaffolded on a
cluster keeps binding there.

## Adopting hosting in an existing project

Env files are scaffolded once and never rewritten, so a project scaffolded
before this default keeps its `_on_cluster` envs. To host one (try it beside
prod first: `forge env new cloud --from prod --bind <name>=hosted` derives the
env and adds the control plane):

1. In the Bundle: `control_plane = forge.ControlPlane {}`, `secret_provider =
   forge.HostedSecrets {}`, `databases = [_db]` with `_db =
   forge.ManagedDatabase {name = "<project>", runtime = forge.OnHosted {}}`.
2. In `_hosted`: `image = w.image[w.image.rfind("/") + 1:] if w.image else
   w.image`, and `DATABASE_URL = forge.DatabaseRef {name = _db.name}` in its
   env (or keep your own Postgres: `forge secret set --env <env> DATABASE_URL`).
3. Declare `_api` (above) and bind `_hosted(_api)` in place of every service
   and worker line; rebind the rest (`migrate`, other jobs) `_hosted(wl.<name>)`.
   A frontend `_on_bucket(...)` → hosted (`image = "<name>"`, `runtime =
   forge.OnHosted {}`, `runtime_config = {API_URL = forge.WorkloadURL
   {workload = "api"}}`). A project scaffolded hosted BEFORE `_api` existed
   binds each service `_hosted(wl.<name>)` and its frontend reaches only the
   first: make the same `_api` edit.
4. Drop the cluster-only fields (`cluster_target`, `network_policy`,
   gateways/routes, `ExternalSecrets`) once nothing is bound to the cluster.

`forge env render <env>` then `forge env new <env> --check` (renders, no
placeholder left, admissible to the control plane). The scaffold's current
shape: `forge project new` in a scratch dir, `deploy/kcl/prod/main.k`.
