# Proposal: bring back an External runtime (deploy a workload through its platform's own CLI: Fly, Cloud Run, ECS, Vercel, …)

**Status:** implemented as `forge.OnExternal` (kcl/workload.k), with the owner's
2026-11-11 decision: four optional ARGV hooks (`status`, `plan`, `deploy`,
`health`) instead of one `deploy_cmd` shell string, no hooks = record-only, and
`forge env verify <env>` to prove parity before forge deploys. The design
points below are answered in `internal/deploytarget/external.go` and the
`deploy/external` skill; credentials (3) are the hook's inherited environment
(`FLY_API_TOKEN` and friends never enter KCL or argv), and frontends (4) are
not covered — `OnExternal` binds workloads only.
**Date:** 2026-10-02
**Origin:** forge#400, moved here on 2026-11-04 and closed. Nothing below has changed since then.

## Summary

`forge.External` was removed in #284 ("one workload model") and nothing replaced it. A workload can no longer be deployed by running a provider's own CLI (`fly deploy`, `gcloud run deploy`, `vercel deploy --prod`, `aws ecs update-service`, a systemd-on-VM script, ...). We should bring it back as a **runtime** on `fw.Workload`, so it sits alongside the other runtimes:

```kcl
# kcl/workload.k:220 today
type Runtime = OnHost | OnCompose | OnCluster | OnHosted | BuildOnly
```

Proposed:

```kcl
type Runtime = OnHost | OnCompose | OnCluster | OnHosted | OnExternal | BuildOnly
```

## Why this matters

- **The model already has a slot for it.** The workload says WHAT runs; the runtime says WHERE. "On a platform forge does not drive natively, through that platform's CLI" is a WHERE, just like `OnHosted`. Ruling it out leaves every Fly / Cloud Run / ECS / Vercel / Railway user with a choice between a cluster they don't want and leaving forge.
- **It is the escape hatch the philosophy promises.** "The 20% are never disempowered" and "opinions with an exit" (README §5) both rely on it.
- **Docs still advertise it.** These are stale today and are being corrected now so they stop promising something that doesn't exist:
  - `README.md:16`: "a Kubernetes deploy path that will just as happily run your `fly deploy`"
  - `README.md:363-364`: "`External` will just as happily run your Fly.io, Cloud Run, ECS, Vercel, Railway, or systemd-on-a-VM command"
  - `kcl/schema.k:972`: a `ControlPlane` docstring compares itself to "the same relationship `External` has with flyctl"
  - Reliant's injected greenfield guidance claimed forge deploys to "Fly, Cloud Run, ECS or Lambda"
- **It was load-bearing for a real project.** Hounders' `FORGE_DEPLOY.md` planned around `forge.External { deploy_cmd = "vercel deploy --prod --yes" }`, verified against an earlier forge with `--dry-run`/`--explain`.

## What the pre-#284 provider did (`cdaa1b20`)

- a generic `sh -c` deploy command (`deploy_cmd`), env-scoped
- honoured `--dry-run`
- recorded last-good tags to `.forge/state/external-<env>-<service>.json`
- rendered zero Kubernetes objects, so no cluster or kubectl context was required

Its rollback hook was later removed with every other rollback path (roll-forward policy). It should **not** come back.

## Design points to settle

1. **Release pinning.** A hosted or cluster deploy ships the digests a release froze. `OnExternal` should hand the command the pinned artifact (e.g. `FORGE_IMAGE=<repo>@<digest>`, `FORGE_RELEASE=<version>`) rather than letting it rebuild. A runtime that rebuilds per env breaks build-once → promote.
2. **Readiness.** `forge env deploy` currently ends in "live, or red". The minimum for External is the command's exit status. An optional `ready_cmd`/`ready_url` would let `forge env status` report something better than "unknown".
3. **Credentials.** Declare the token env var by name (`token_env = "FLY_API_TOKEN"`, as `ControlPlane.token_env` does), so the value never lands in KCL or argv.
4. **Frontends.** Decide whether `FrontendRuntime` gets the same member (`vercel deploy` / `netlify deploy` for a static build) or whether that stays `OnBucket` / `OnFirebase` / `OnHosted`.
5. **Recipes.** Restore copy-pasteable KCL for Fly, Cloud Run, ECS, Vercel, Railway and systemd as a skill (`deploy/external`), and make sure `forge env deploy --help` and the `deploy` skill's runtime table name it.

## Acceptance

- `wl.api | {runtime = forge.OnExternal {deploy_cmd = "fly deploy --image $FORGE_IMAGE"}}` renders, `forge env deploy <env> --dry-run` prints the command it would run, and a real deploy runs it with the release-pinned image.
- An env that binds only `OnExternal` workloads needs no cluster and no kubectl context.
- README §5 and the `deploy` skill can truthfully name the targets again.
