---
name: deploy
description: Ship anything — static sites, SPAs, services, workers: declare each workload once, bind it to a runtime per env (host, cluster, hosted), build, deploy, verify, roll forward. A frontend-only site: deploy/static-site.
---

# Ship It

**New projects ship to Reliant hosting by default** — `staging` and `prod` bind
everything to it, and `forge env deploy prod` is the whole procedure. What it
provides, what it refuses, and the gates (billing, unset secrets):
`deploy/hosting`. A cluster you operate is the secondary path, below.

**No Reliant account yet?** `forge env deploy <env> --claimable` publishes
anyway and prints a live URL plus a claim link for your user (see
`deploy/hosting`, "No account: `--claimable`"; an unclaimed backend has no outbound internet, so an LLM call goes through Reliant's gateway: `deploy/llm`). It needs no `forge login`.

## The model: declare once, bind per env

Everything that runs is a `fw.Workload`, declared ONCE in
`deploy/kcl/workloads.k`. Each env's `deploy/kcl/<env>/main.k` BINDS every
workload to one runtime — `forge.OnHost`, `forge.OnCluster`, `forge.OnHosted`,
`forge.OnCompose`, `forge.BuildOnly`, or `forge.OnExternal` (your own deploy
tool, driven by forge: `deploy/external`) — and states only its own values; one
env may mix runtimes, and a workload with no runtime is a render error.
`output = forge.render(bundle)` is the ONE entrypoint, so always go through
`forge env render` / `forge env deploy`. The scaffolded `dev` runs on the
host; `staging` and `prod` are hosted on Reliant, and hosting elsewhere is a
one-line rebind. Runtimes, binders, `forge env new --from`, what forge writes
into every spec, the restricted profile, per-env config: load `deploy/model`.

## The commands

Environment is a positional arg — `forge env deploy dev`, not `forge env deploy --env dev`.

```
forge env up dev                  # the local loop: build + cluster apply + host launch + frontend dev
forge build <env>                 # what <env> declares (host workloads need no image)
forge env render prod             # every object, and the cluster it lands on — changes nothing
forge env shape prod              # what the env IS, not its objects — see deploy/shape
forge env deploy prod             # SHIP THIS CHECKOUT: build → push → cut → plan → confirm → apply → wait
forge env deploy prod --yes       # the same, in CI: "I read the plan" (no TTY to prompt on)
forge env deploy prod v1.7.1      # deploy a release that already exists; builds nothing
forge env deploy prod --dry-run   # render/print without applying
forge env status prod             # verify: bound release, running digests, rollout, health
forge env verify prod             # would a deploy change anything? read-only parity, per workload
```

Nothing is written until the plan is confirmed: with no terminal and no
`--yes`, deploy exits **5** having built, pushed and cut. There is no rollback:
recovery is a new release that rolls forward. The whole procedure: `deploy/release`.

## Where to go next

| You need… | Load |
|---|---|
| runtimes, the scaffolded envs and binders, `forge env new`, probes/grace/PDB/strategy forge writes, per-env config, cross-references, derived features | `deploy/model` |
| pre-flight checks, `forge env build <env> --push` and `forge build <env>` with their flags, the project image, registry login, the config check, Docker build contexts | `deploy/build` |
| the two deploy forms, the confirm gate, scoped deploys, the ledger, preflight, pre-rollout migrations, verify + recover | `deploy/release` |
| an existing app in any language, deployed from its own Dockerfile (templates, then tiers) | `adopt/deploy` |
| keep Helm / Fly.io / Cloud Run / ECS / Argo CD and let forge drive it; prove parity first with `forge env verify` | `deploy/external` |
| the local loop: `forge env up` flags, host runners, `--target` | `dev` |
| Reliant hosting: what it wires, refuses, bills; `--claimable` | `deploy/hosting`, `deploy/hosted-capacity`, `deploy/hosted-registry`, `deploy/lifecycle` |
| a frontend's runtime (`OnHost`, `OnHosted`, `OnBucket`, `OnFirebase`), runtime config; a site alone | `deploy/frontends`, `deploy/static-site` |
| a custom hostname | `deploy/domains` |
| reading the plan; release gates (`health_gate_*`, signing) | `deploy/plan`, `deploy/delivery-policy` |
| what an env declares, and who applies it (`applier`, `hub_converges`) | `deploy/shape`, `deploy/flux`, `deploy/flux-safe`, `deploy/delivery-units` |
| routes and per-route traffic policy | `deploy/routes` |
| extra Kubernetes objects (`Bundle.manifests`), your own chart or YAML | `deploy/byo-yaml` |
| changing one field of an object forge renders (`Bundle.overrides`) | `deploy/overrides` |
| another cluster's API server; letting the control plane deploy into yours | `deploy/cluster-access`, `deploy/cluster-connect` |
| secrets beyond `config_secrets` | `deploy/control-plane-secret`, `deploy/external-secrets`, `deploy/openbao` |
| users and sign-in declared in KCL | `deploy/managed-identity` |
| PDBs, priority classes, StatefulSet volume growth | `deploy/pdb`, `deploy/pod-priority`, `deploy/statefulset-volumes` |
| a KCL value from a script; another project's `workloads.k` | `deploy/kcl-eval`, `deploy/kcl-imports` |
| several repos, any language: a shared infra/envs repo, or one deploy repo building N app repos at pinned refs | `deploy/polyrepo` |
| "never deploy older than X"; a `below_rollback_floor` refusal; declaring an API/feature/data change that cannot be rolled back past | `deploy/rollback-floor` |
| a cut that died mid-push; control plane unreachable; the k3d registry mirror | `deploy/interrupted-cut`, `deploy/offline-ledger`, `deploy/k3d-registry` |
| your own CI deploys; forge only records each deploy (`forge release record`) for monitoring | `deploy/record` |

## Rules

- Declare a workload once, in `workloads.k`. An env file that re-states a
  workload's command, ports or build is a second copy that will drift.
- Never `kubectl apply` a hand render — everything through `forge env deploy`.
  To read a VALUE KCL declares, `forge kcl eval`; see `deploy/kcl-eval`.
- Deploying another forge project's workloads? Import its `workloads.k`
  (`[forge_projects]` in kcl.mod: at the go.mod pin, or by a git pin for a
  repo that is not a Go dependency) instead of copying it; see
  `deploy/kcl-imports`. Code in another repo is a pinned `forge.GitSource`,
  never a `../sibling` path: `deploy/polyrepo`.
- Image tags are immutable — commit SHA by default, digest-pinned on deploy.
- Secrets never live in KCL — it is checked in. Reference them
  (`config_secrets`, `forge.SecretRef`, `forge.ManagedSecret`).
- EVERY image on a CLUSTER or COMPOSE workload names its registry host
  (`ghcr.io/acme/api`). A hostless one is refused at render, naming the
  workload: no env registry exists to complete it. A `forge.OnHosted` workload
  may name a bare image — forge composes the address from the env.
- A hosted env never accepts raw objects (`Bundle.manifests`): the platform
  owns the render.
- Roll forward, never back: a failed deploy leaves the previous release
  serving, and the fix ships as a new release.
