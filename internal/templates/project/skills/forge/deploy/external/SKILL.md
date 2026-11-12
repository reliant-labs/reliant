---
name: deploy/external
description: Keep your own deploy tooling (Helm, Fly.io, Cloud Run, ECS, Argo CD, a script) and let forge drive it — forge.OnExternal's four argv hooks (status, plan, deploy, health), worked recipes for each tool, and the shadow-mode path that proves parity with `forge env verify` before forge deploys anything.
---

# Your deploy tooling, driven by forge: `forge.OnExternal`

A workload bound to `forge.OnExternal` is deployed by **the tool you already
use** — Helm, `fly deploy`, `gcloud run deploy`, an ECS service update, an Argo
CD sync, a script. forge keeps everything around that step: it builds and pushes
the image (when the workload declares a build), cuts and records the release,
promotes it in the env's ledger, records the deploy attempt, hands your tool the
**frozen digest**, and marks the deploy in observability (`forge obs changes`,
`forge obs check` key on the promotion). Nothing about your tool changes.

It is a runtime **binding**, like `forge.OnCluster`: the workload is declared
once in `deploy/kcl/workloads.k`; staging may bind it `OnCluster` while prod
binds it `OnExternal`. forge renders no manifests for it.

**Why bother, if your pipeline already deploys?** What you get the day you
bind it — before forge deploys anything:

- `forge env verify prod` — a read-only proof that what forge would ship is
  what runs (image digest AND your tool's own diff), per workload.
- One release ledger across every runtime: `forge env status prod` reads an
  external workload's running digest through its `status` hook, beside the
  cluster's; `forge env status prod --history` lists every promotion.
- Deploy markers for monitoring: every promotion is the marker
  `forge obs check` judges a release by.
- Build-once-promote-by-digest: staging's exact bytes reach prod; your tool
  receives `repo@sha256:…`, never a moving tag.

## The binding

```kcl
# deploy/kcl/prod/main.k
_ext = forge.OnExternal {
    status = ["./scripts/forge/status.sh"]                 # read-only: prints {"digest": …}
    plan   = ["helm", "diff", "upgrade", "api", "./chart", "-n", "prod",
              "--set", "image.ref=$FORGE_IMAGE", "--detailed-exitcode"]
    deploy = ["helm", "upgrade", "--install", "api", "./chart", "-n", "prod",
              "--set", "image.ref=$FORGE_IMAGE", "--wait"]
    health = ["kubectl", "-n", "prod", "rollout", "status", "deploy/api", "--timeout=5m"]
    env = {KUBECONFIG = "/etc/kube/prod"}                  # optional, every hook
}

output = forge.render(forge.Bundle {
    project = "acme"
    workloads = [wl.api | {runtime = _ext}]
})
```

Every field is optional. **No hooks at all is record-only**: forge builds,
cuts and promotes; your CI (or a person) deploys. That is the passive end of
the same binding — start there and add hooks one at a time.

| hook | when | contract |
|---|---|---|
| `status` | `forge env verify`, `forge env status`, before and after each deploy | read-only; prints ONE JSON object on stdout: `{"digest": "sha256:…", "version": "…", "image": "…", "health": "healthy\|degraded\|absent\|unknown"}`. Only `digest` matters for parity; `repo@sha256:…` is accepted. Logs go to stderr. |
| `plan` | `forge env verify` | read-only dry run; **exit 0 = no change, 2 = changes**, anything else = error (the `helm diff --detailed-exitcode` / `terraform plan -detailed-exitcode` convention). Its output is shown verbatim, never parsed. |
| `deploy` | `forge env deploy` | ships `$FORGE_IMAGE`; exit 0 = shipped. |
| `health` | after `deploy` succeeds | exit 0 = healthy; non-zero fails the deploy. |

Other fields: `env` (extra env for every hook; `FORGE_*` names are refused),
`pass_env` (default False — also pass the workload's literal env and every
secret the env's secret provider resolves by name), `working_dir` (relative to
the project root, the default), `timeout` (seconds per hook, default 900),
`platform` (`amd64` default, or `arm64`: the arch forge builds the image for).

### No shell, and the `$FORGE_*` variables

Hooks are **argv**, exec'd directly: nothing is word-split, quoted or globbed,
and a substituted value is always exactly one argument. Need a pipe? Put it in
a script (or `["sh", "-c", "…"]`, knowing you have opted into a shell).

forge sets these for every hook and substitutes them inside argv elements —
write `$FORGE_IMAGE` (KCL itself interpolates a dollar-brace in a normal
string; inside a raw string, `r"${FORGE_IMAGE}"` works too). Only `FORGE_*`
names forge sets are substituted; `$HOME` reaches your tool untouched.

| variable | value |
|---|---|
| `FORGE_ENV`, `FORGE_WORKLOAD` | env and workload name |
| `FORGE_PROJECT`, `FORGE_PROJECT_DIR` | project name, project root (hooks run there) |
| `FORGE_IMAGE` | the image to run, `repo@sha256:…` when pinned |
| `FORGE_DIGEST` | `sha256:…` alone ("" when unpinned) |
| `FORGE_VERSION` | the image's version tag |
| `FORGE_RELEASE` | the release the env is promoted to |
| `FORGE_PREVIOUS_DIGEST` | what `status` reported running before this deploy ("" without `status`) |
| `FORGE_HOOK` | `status`, `plan`, `deploy` or `health` |

Precedence: your `env`, then (with `pass_env`) the workload's env, then
`FORGE_*`, which always wins.

## Adopting forge on a project something else deploys (~1 hour)

Each step stands alone and changes nothing that is running. Quote this to the
user as the plan; the times are for one service with a working deploy today.

1. **Declare the workload** (10 min). `deploy/kcl/workloads.k`: name, image
   with its registry (`ghcr.io/acme/api`), and its build if forge should build
   it (`forge.DockerBuild {dockerfile = "Dockerfile"}`) — or no build, and forge
   records what your CI pushed.
2. **Bind it record-only** (5 min). `wl.api | {runtime = forge.OnExternal {}}`
   in `deploy/kcl/prod/main.k`; `forge env render prod` must succeed.
3. **Add `status`** (15 min). A script printing `{"digest": …}` from the live
   system (recipes below). Check it alone: `FORGE_WORKLOAD=api ./scripts/forge/status.sh`.
4. **Prove identity parity** (5 min). Before forge has a release, tell verify
   what you expect: `forge env verify prod --image api=ghcr.io/acme/api@sha256:<what CI deployed>`.
   `match` means forge reads your system correctly.
5. **Add `plan`** (15 min) and re-run verify until config says `match` — your
   tool sees no change between what runs and what forge would hand it.
6. **Add `deploy` and `health`** (10 min), then let forge drive:
   `forge env deploy prod`. It builds, pushes, cuts, promotes, runs your
   `deploy` hook with the new digest, then `health`, then re-reads `status`.
7. **Retire the old CI deploy step**: CI runs `forge env deploy prod --yes`.
   Afterwards `forge env verify prod` (no `--image`) checks against the promotion.

`forge env verify` builds, pushes, records and deploys **nothing** — it is safe
on prod at any time. Exit **0** all match, **1** drift, **2** something could
not be verified. A missing `status` or `plan` hook is reported **unverified**,
never as "no diff". Cluster-bound workloads are checked too: image digest off
the cluster, config by the client-side live diff of the release's recorded
bundle ("not verifiable without a build" when none is recorded yet).

## Recipes

Each is a starting point: names, regions and labels are the user's. Check each
hook alone before binding it (`FORGE_WORKLOAD=api FORGE_IMAGE=… ./script`).

### Helm (any Kubernetes cluster)

```kcl
_helm = forge.OnExternal {
    status = ["./scripts/forge/k8s-status.sh"]
    plan   = ["helm", "diff", "upgrade", "api", "./chart", "-n", "prod", "--set", "image.ref=$FORGE_IMAGE", "--detailed-exitcode"]
    deploy = ["helm", "upgrade", "--install", "api", "./chart", "-n", "prod", "--set", "image.ref=$FORGE_IMAGE", "--wait"]
    health = ["kubectl", "-n", "prod", "rollout", "status", "deploy/api", "--timeout=5m"]
}
```

`scripts/forge/k8s-status.sh` (needs `jq`; `helm diff` is the
[helm-diff](https://github.com/databus23/helm-diff) plugin):

```sh
#!/bin/sh
set -eu
kubectl -n prod get deploy "$FORGE_WORKLOAD" -o json | jq -c '{
  digest: ((.spec.template.spec.containers[0].image | split("@"))[1] // ""),
  image:  .spec.template.spec.containers[0].image,
  health: (if (.status.readyReplicas // 0) == .spec.replicas then "healthy" else "degraded" end)
}'
```

A chart deployed by tag has no digest in its spec: read the pod's resolved
digest instead (`kubectl get pods -l app.kubernetes.io/name=api -o
jsonpath='{.items[0].status.containerStatuses[0].imageID}'`, then strip to
`sha256:…`). Pinning the chart to `image.ref=$FORGE_IMAGE` is what makes
`plan` meaningful: helm then diffs digest to digest.

### Fly.io

```kcl
_fly = forge.OnExternal {
    status = ["./scripts/forge/fly-status.sh"]
    deploy = ["fly", "deploy", "--app", "acme-api", "--image", "$FORGE_IMAGE"]
    health = ["fly", "checks", "list", "--app", "acme-api"]
}
```

```sh
#!/bin/sh
set -eu
fly image show --app acme-api --json | jq -c '{digest: ([.[] | .Digest // .digest] | first // ""), health: "unknown"}'
```

Fly has no dry run, so leave `plan` unset: verify proves identity and reports
config **unverified** — honest, not "no diff". Auth: `FLY_API_TOKEN`. Check
the `fly image show --json` shape against the user's flyctl version before
relying on the jq path.

### Google Cloud Run

```kcl
_run = forge.OnExternal {
    status = ["./scripts/forge/run-status.sh"]
    plan   = ["./scripts/forge/run-plan.sh"]
    deploy = ["gcloud", "run", "deploy", "api", "--image", "$FORGE_IMAGE", "--region", "us-central1", "--quiet"]
}
```

```sh
#!/bin/sh
# run-status.sh — Cloud Run records the digest it resolved on the revision.
set -eu
rev=$(gcloud run services describe api --region us-central1 --format='value(status.latestReadyRevisionName)')
d=$(gcloud run revisions describe "$rev" --region us-central1 --format='value(status.imageDigest)')
printf '{"digest":"%s","version":"%s"}\n' "${d##*@}" "$rev"
```

```sh
#!/bin/sh
# run-plan.sh — exit 0 when the running revision already runs $FORGE_IMAGE, 2 when not.
set -eu
cur=$(./scripts/forge/run-status.sh | jq -r .digest)
[ "$cur" = "$FORGE_DIGEST" ] && { echo "no change"; exit 0; }
echo "image: $cur -> $FORGE_DIGEST"; exit 2
```

Cloud Run's `--image repo@sha256:…` deploys exactly those bytes; `--quiet`
keeps it non-interactive. The plan above checks the image only; extend it with
`gcloud run services describe --format=export` diffed against a checked-in
`service.yaml` when the service's config matters too.

### AWS ECS

```kcl
_ecs = forge.OnExternal {
    status = ["./scripts/forge/ecs-status.sh"]
    plan   = ["./scripts/forge/ecs-plan.sh"]
    deploy = ["./scripts/forge/ecs-deploy.sh"]
    health = ["aws", "ecs", "wait", "services-stable", "--cluster", "prod", "--services", "api"]
}
```

```sh
#!/bin/sh
# ecs-status.sh — the digest the running tasks resolved.
set -eu
arns=$(aws ecs list-tasks --cluster prod --service-name api --desired-status RUNNING --query 'taskArns' --output text)
aws ecs describe-tasks --cluster prod --tasks $arns --output json |
  jq -c '{digest: ([.tasks[].containers[] | select(.name=="api") | .imageDigest] | first // ""), health: "unknown"}'
```

```sh
#!/bin/sh
# ecs-deploy.sh — register a task-definition revision with $FORGE_IMAGE and roll the service.
set -eu
td=$(aws ecs describe-services --cluster prod --services api --query 'services[0].taskDefinition' --output text)
aws ecs describe-task-definition --task-definition "$td" --query taskDefinition --output json |
  jq --arg img "$FORGE_IMAGE" '(.containerDefinitions[] | select(.name=="api") | .image) = $img
     | del(.taskDefinitionArn,.revision,.status,.requiresAttributes,.compatibilities,.registeredAt,.registeredBy)' > /tmp/td.json
new=$(aws ecs register-task-definition --cli-input-json file:///tmp/td.json --query 'taskDefinition.taskDefinitionArn' --output text)
aws ecs update-service --cluster prod --service api --task-definition "$new" >/dev/null
```

`ecs-plan.sh` follows the Cloud Run one: compare `ecs-status.sh`'s digest with
`$FORGE_DIGEST`, exit 0 or 2.

### Argo CD (GitOps)

```kcl
_argo = forge.OnExternal {
    status = ["./scripts/forge/argo-status.sh"]
    plan   = ["./scripts/forge/argo-plan.sh"]
    deploy = ["argocd", "app", "set", "api", "-p", "image.ref=$FORGE_IMAGE"]
    health = ["argocd", "app", "wait", "api", "--sync", "--health", "--timeout", "600"]
}
```

```sh
#!/bin/sh
# argo-status.sh
set -eu
argocd app get api -o json | jq -c '{
  digest: ((.status.summary.images[0] // "" | split("@"))[1] // ""),
  health: (.status.health.status | ascii_downcase | if . == "healthy" then "healthy" elif . == "missing" then "absent" elif . == "degraded" then "degraded" else "unknown" end)
}'
```

```sh
#!/bin/sh
# argo-plan.sh — `argocd app diff` exits 1 on a diff; forge's contract is 2.
argocd app diff api; rc=$?
case $rc in 0) exit 0 ;; 1) exit 2 ;; *) exit "$rc" ;; esac
```

If the app's parameters live in a git repo instead (the GitOps-pure setup),
`deploy` is a script that commits `$FORGE_IMAGE` to that repo and pushes;
Argo then converges on its own, and `health` (`argocd app wait`) is what makes
forge wait for it. A deploy hook that returns before the sync lands is fine:
forge re-reads `status` afterwards and only WARNS when the digest has not moved
yet.

### Any other tool

A script per hook. The pattern: `status` asks the system what runs and prints
the JSON; `plan` renders what would change and exits 0/2 (wrap tools whose
"changes" code is 1, like `kubectl diff`, as the Argo plan does); `deploy`
applies `$FORGE_IMAGE`; `health` blocks until it serves.

## Rules

- Prefer argv to `sh -c`: with no shell, a value can never become a second
  command. Put pipelines in a script file checked into the repo.
- `status` and `plan` must be READ-ONLY: `forge env verify` promises the user
  it changes nothing, and it runs them on prod.
- Pin by digest in your tool (`--image $FORGE_IMAGE`, `image.ref=$FORGE_IMAGE`).
  A tool that re-resolves a tag can deploy bytes the release never recorded.
- There is no rollback hook: recovery is roll forward — ship the fix as a new
  release through the same `deploy` hook.
- Never "verify" by leaving `plan` out: a missing hook is reported unverified,
  and that is the honest answer.

Related: `deploy` (the model), `deploy/release` (the deploy procedure and the
ledger), `adopt` (bringing an existing app to forge).
