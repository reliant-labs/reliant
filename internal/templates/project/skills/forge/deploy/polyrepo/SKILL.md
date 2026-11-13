---
name: deploy/polyrepo
description: forge across several repositories, in any language — (a) each repo its own forge project sharing an envs/infra repo's KCL by git pin (`[forge_projects] x = { repo, ref, commit }`, no go.mod), or (b) one deploy repo building N app repos' images (`DockerBuild.context = forge.GitSource`). Pins, the shared source cache, the local override file, offline renders, the provenance a release records, and what each step costs.
---

# forge across repositories

Nothing about forge needs a monorepo, or Go. Two topologies cover what teams
actually run, and they combine:

| Topology | Shape | Pick it when |
|---|---|---|
| **(a) Shared infra repo** | every app repo is its own forge project; an `infra` (or `envs`) repo holds the shared KCL: clusters, the hosted control plane, env-wide defaults, binders | each team ships its own service on its own schedule, and the platform team owns what "staging" means |
| **(b) Deploy repo** | one forge project declares every workload; their code lives in N app repos, built from each repo's own Dockerfile at a pinned ref | one team ships the whole system, releases go out together, and the app repos should know nothing about deploys |

Both are plain declarations in KCL and kcl.mod, reviewed in a PR like
anything else. Neither needs a CI step that checks out a second repo.

## What you get

- **A pin you can review.** `repo` + `ref` (and an optional `commit` lock) says
  exactly which code a deploy uses. Bumping it is a one-line diff, and a
  release that ran at another commit cannot pass for this one.
- **The same bytes everywhere.** A pin is fetched once into a machine-local
  cache keyed by repo and ref, and a laptop, a container and CI resolve it
  identically. A KCL import's `commit` lock, or a full-sha `ref` anywhere,
  makes a moved tag an error rather than a different build. A sibling checkout path (`../billing`) builds whatever
  happens to be checked out there.
- **Local iteration without unpinning.** `.forge/source-overrides.yaml`
  (gitignored, never committed) points a repo at your working copy. Every use
  of it prints `NOT the pinned ref`, and CI never sees it.
- **Provenance in the ledger.** An image built from another repo records that
  repo and its commit in its build state and on the release (`built_from`), so
  "what billing code is in prod?" is answered from the release alone.
- **Release safety.** A release refuses to cut while any pin (a DockerBuild
  context or Dockerfile, a KCL import) is served by a local override, naming
  it, so a release is always rebuildable from its pins.
- **Any language.** The app repos need only a Dockerfile. The shared KCL repo
  needs only a `forge.yaml` and its `deploy/kcl`. No go.mod anywhere.

## The pin, the cache, the override

Every cross-repo declaration in forge is a `forge.GitSource`, resolved by one
resolver (`docs/cross-repo-sources.md` in forge has the design):

```python
forge.GitSource {repo = "github.com/acme/billing", ref = "v2.3.1", subdir = "svc"}
```

- `repo`: `host/owner/name` (expanded to https), or an `https://`, `ssh://` or
  `git@host:owner/name` URL used verbatim, so private repos authenticate the
  way your git already does (ssh keys, a credential helper). forge never
  prompts: in CI give git a token before the build.
- `ref`: a tag, a branch or a full sha. Required: forge never defaults to a
  default branch. A sha cannot move. A branch is cached like anything else and
  NOT re-fetched per build; bump the ref, or refresh (below), to move.
- Cache: `<UserCacheDir>/forge/sources/<repo>-<digest>`. A hit costs no
  network, so **renders work offline once each pin was fetched on that
  machine**. A miss that cannot fetch fails naming the pin and both remedies.
- Override, for working on two repos at once:

  ```yaml
  # .forge/source-overrides.yaml in the CONSUMING project — never committed
  sources:
    github.com/acme/infra: ../infra
    github.com/acme/billing: ../billing
  ```

  Keyed by repo, so one line covers every use and survives a ref bump. A
  missing directory is an error, never a silent fall back to the pin.

## (a) Each repo is a forge project, sharing an infra repo

The infra repo is a forge project with no app in it: a `forge.yaml` naming the
forge it pins, and the shared KCL.

```
infra/                         # github.com/acme/infra
  forge.yaml                   # name: infra; forge_version: the same pin as every app repo
  deploy/kcl/kcl.mod
  deploy/kcl/envs.k            # shared: clusters, control plane, binders, defaults
```

```python
# infra deploy/kcl/envs.k
import forge
import forge.workloads as fw

control_plane = forge.ControlPlane {}
staging_runtime = forge.OnHosted {}
hosted = lambda w: fw.Workload -> fw.Workload {
    w | {runtime = forge.OnHosted {}}
}
```

Each app repo imports it by **git pin** in its own kcl.mod:

```toml
# billing deploy/kcl/kcl.mod
[forge_projects]
infra = { repo = "github.com/acme/infra", ref = "v1.4.0", commit = "8f2c1e0b9d4a" }
```

```python
# billing deploy/kcl/staging/main.k
import forge
import infra.envs as envs
import ..workloads as wl

output = forge.render(forge.Bundle {
    project = "billing"
    control_plane = envs.control_plane
    workloads = [envs.hosted(wl.api)]
})
```

- `subdir` defaults to `deploy/kcl`. The infra repo's `forge.yaml` may sit at
  the repo root or any directory above `subdir`.
- `commit` is optional and is the lock: the ref must resolve to it (a prefix
  of 7+ hex digits is enough). Without it a re-pointed tag would render
  different KCL on a machine that fetched later. A `ref` that is a full sha is
  its own lock. Recommended: a tag for readability plus `commit`.
- `forge kcl imports` lists every import with the commit it resolved to, and
  fetches every pin not yet cached: run it once to prepare a machine (or a CI
  cache) for offline renders. `forge kcl imports --refresh` re-fetches (a
  moved branch), and a lock mismatch then says which commit the ref moved to.
- **Forge versions must agree.** The imported KCL's `import forge` is the
  rendering forge's, so a skewed `forge_version` is refused with both pins and
  the order to fix them in (infra first). Bump forge in the infra repo, tag it,
  then move each app's `ref`.
- A project that already `require`s the other repo as a Go module keeps
  `module = "<go module>"`: the version then comes from go.mod, with no second
  pin. `repo` and `module` are mutually exclusive per import.

**Cost: easy, ~1 hour for the first app repo, ~15 min for each further one.**
Create the infra repo and move the shared KCL into it, 30 min. Tag it, 5 min.
Add the `[forge_projects]` line and switch each env's `main.k` to the shared
names, 15 min per repo. `forge env render <env>` in each repo shows the same
objects as before, 5 min.

## (b) One deploy repo, N app repos

The deploy repo is a forge project with no app code. Each workload builds
another repo's Dockerfile at a pinned ref:

```python
# deploy-repo deploy/kcl/workloads.k
import forge
import forge.workloads as fw

_billing = forge.GitSource {repo = "github.com/acme/billing", ref = "v2.3.1"}

billing = fw.Workload {
    name = "billing"
    kind = "service"
    image = "billing"
    # The other repo's tree is the build context, and its own Dockerfile
    # (`<context>/Dockerfile`) builds it.
    build = forge.DockerBuild {context = _billing}
    ports = [fw.Port {name = "http", port = 8080, expose = True}]
}

search = fw.Workload {
    name = "search"
    kind = "service"
    image = "search"
    build = forge.DockerBuild {
        context = forge.GitSource {repo = "github.com/acme/search", ref = "9b1d0c7e5f3a2b4c6d8e0f1a2b3c4d5e6f7a8b9c", subdir = "server"}
        dockerfile = "deploy/Dockerfile"   # relative to the pinned context
    }
}
```

Which Dockerfile:

| You declare | forge runs |
|---|---|
| `context = GitSource`, no `dockerfile` | `-f <checkout>/Dockerfile <checkout>` |
| `context = GitSource`, `dockerfile = "docker/prod.Dockerfile"` | that path INSIDE the pinned context (escaping it is refused) |
| `dockerfile = GitSource {…, subdir = "py/Dockerfile"}` | that file from its own pinned repo (a shared Dockerfile repo) |
| your OWN Dockerfile over another repo's code | keep `context` in this repo and name the other repo as a named context, `build_contexts = {app = forge.GitSource {…}}` with `COPY --from=app` (`deploy/build-contexts`) |

- `forge build <env> --plan` resolves the pins and prints each docker step
  with the directory it sends.
- A release records each image's `built_from` (repo + commit), and refuses to
  cut while a pin is overridden to a working copy.
- `forge doctor --verbose` lists every pin across envs without fetching.
- To ship a new app version: bump its `ref` in one PR. That PR is the change
  log of what the deploy repo ships.

**Cost: easy, ~1–2 hours for three app repos.** Create the deploy repo
(`forge.yaml`, `deploy/kcl` from the `adopt` templates), 30 min. One workload
per app repo with a pinned `context`, 10 min each. Make each app's Dockerfile
build on its own (no host-built `dist/`), 0–30 min each. `forge build
staging --plan`, then `forge env deploy staging`, 15 min.

## Combining them

A deploy repo (b) can import an infra repo (a) for its env shape, and an app
repo can be built by a deploy repo AND deploy itself to a preview env. Each
pin is independent; the override file covers all of them by repo.

## Troubleshooting

- **`pin mismatch`**: the ref resolved to another commit than `commit` (or
  the sha `ref`). The ref moved, or this machine cached an older one. Run
  `forge kcl imports --refresh`, and if the new commit is intended, update
  `commit` in kcl.mod in a reviewed PR.
- **`could not be resolved`** / **`fetch …`**: offline on a cold cache, a typo
  in `repo`/`ref`, or git cannot authenticate. Fix git's access, or override to
  a local checkout.
- **`not a forge project`**: the imported repo needs a `forge.yaml` with
  `forge_version` at or above its `subdir`.
- **`version skew`**: align `forge_version` across repos, infra first.
- **`there is no go.mod`** for a `module =` import: use `repo` + `ref`.

## Rules

- Never commit `.forge/source-overrides.yaml`; never point a pin at a sibling
  path in KCL (`path = "../x"`, `context = "../x"`). Pin it.
- Prefer a tag plus `commit`, or a sha, over a branch.
- One declaration per fact: shared KCL lives in the infra repo and is
  imported, not copied.
