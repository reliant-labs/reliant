# Cross-repo sources: building a component whose code lives in another repo

Some components do not live in the repository that declares them. The
clearest case is a frontend: control-plane declares `reliant-web`, the
primary customer-facing SPA, but the code is in the `reliant` repository's
`web/` directory.

Until this existed, the only way to say that was a filesystem path:

```python
forge.Frontend {
    name = "reliant-web"
    path = "../reliant/web"  # works on a laptop, absent in CI
}
```

This document describes what replaced it, and why the replacement is a
pinned source rather than a checkout step in CI.

The implementation lives in:

- `internal/gitsource/` — the resolver, the cache, the overrides file
- `internal/config/config.go` — the shared `GitSource` type
- `kcl/schema.k` — the KCL schema (`forge.GitSource`)
- `internal/cli/frontend_source.go` — the build/deploy resolution seam

## The problem is reproducibility, not just CI

The obvious failure is that `../reliant/web` does not exist in CI, where
`actions/checkout` clones one repository, so the build dies:

```
frontends[name=reliant-web] (type=vite-spa) declared in deploy/kcl but path
"../reliant/web" does not exist (expected at /tmp/.../reliant/web)
```

The less obvious failure is the one that matters more. Where the sibling
checkout _is_ present, the build succeeds — and ships whatever happened to
be checked out in that directory. There is no pin, so two machines with
different `../reliant` states produce different artifacts from identical
commits. A deploy is not reproducible, and nothing reports that.

The asymmetry states it best. control-plane's **Go** dependency on reliant
is a pinned, proxy-resolved module:

```
require github.com/reliant-labs/reliant v1.6.2
```

Its **frontend** dependency on the same repository was an unpinned
filesystem path. Same dependency, same repo, two entirely different levels
of rigor.

## Why not just check out the sibling repo in CI

That workaround exists — a second `actions/checkout` with `repository:` and
`path: ../reliant` — and it does make the build pass. It is not enough for
two reasons.

It is imperative glue that duplicates the declaration. The KCL already says
this project depends on reliant; the workflow file says it again, in a
different language, and the two drift silently. Worse, neither of them
agrees with the pinned Go version, so a project can ship a frontend built
from `main` against a backend pinned to `v1.6.2` with nothing flagging it.

And it re-introduces the thing forge's philosophy targets directly: state
of the world that must be set up, in the right order, before the
declarative layer runs. A step that must be _run_ is a step that can be
skipped, half-completed, or lost when a teammate clones the repo.

## The declaration

```python
forge.Frontend {
    name = "reliant-web"
    type = "vite"
    source = forge.GitSource {
        repo = "github.com/reliant-labs/reliant"
        ref = "v1.6.3"          # tag, branch, or commit sha
        subdir = "web"
    }
    public_dir = "dist"
    runtime = forge.OnFirebase { project = "reliant-labs-475814", site = "reliant-prod" }
}
```

`repo` accepts the canonical `host/owner/name` shorthand, which forge
expands to an https clone URL, or an explicit `https://` / `ssh://` /
`git@host:owner/name` URL used verbatim — a private host must not be forced
through a spelling forge invented.

`ref` is **required**. forge does not default to a repository's default
branch, because an unpinned cross-repo dependency is exactly the problem
this feature solves.

`path` and `source` are mutually exclusive. With both set there are two
answers to "where is this frontend's code", and whichever forge silently
preferred, the other would be a lie that reads as truth in review.

## The cache

A fetch lands in a machine-local cache under
`<UserCacheDir>/forge/sources/<repo-slug>-<digest>`, keyed by repo **and**
ref. A second build of the same pin does no network work at all.

Two properties of the key are deliberate:

- **Ref is in the key**, so two pins of one repository coexist rather than
  fighting over one directory. Bumping a ref in one environment cannot
  silently change another.
- **Subdir is not**, so a project consuming two directories of one
  repository fetches the repository once and takes two views into it.

A materialized entry records what produced it in `.forge-source.json`,
written last. Its presence is what makes a directory a cache hit, so a
fetch interrupted halfway leaves nothing a later build mistakes for a
complete checkout.

Fetches are shallow (`--depth 1`) for one ref rather than full clones,
with an automatic unshallowed retry when the server refuses — fetching a
bare commit sha requires `uploadpack.allowReachableSHA1InWant`, which many
hosts do not enable, and a pin by sha is the most reproducible thing a user
can write. It must not be the one that fails.

### Refs that move

A commit sha, or a tag under a project that does not move tags, is
immutable: the cache can never be stale.

A **branch** ref is cached the same way — forge does not re-fetch a branch
on every build. That is intentional, and it is the same reasoning as
everything above: a dependency that silently changes underneath a build is
the non-reproducibility this feature exists to remove. To move, bump the
ref, or drop the cache entry.

### What the build is told about itself

The fetch is shallow and carries no tags, so a build pinned to `v1.8.1`
sees a detached sha: `git describe` answers with a hash, and the build
cannot name its own version. So forge tells it. Every frontend build forge
runs (`forge env deploy` for Firebase / bucket / build-only, and the hosted
static build) gets:

| Variable              | Value                                                    |
| --------------------- | -------------------------------------------------------- |
| `FORGE_SOURCE_REPO`   | the GitSource `repo` as declared                         |
| `FORGE_SOURCE_REF`    | the GitSource `ref` — the pin: a tag, a branch, or a sha |
| `FORGE_SOURCE_COMMIT` | the sha that ref resolved to when it was fetched         |
| `FORGE_RELEASE`       | the release version the deploy is bound to               |

All four are always present and EMPTY when forge does not know the value —
an in-repo frontend, an unbound deploy, or a source standing in for its pin
through a local override (below), where naming the pin would misreport what
was built. Empty overrides any stale value in the calling shell, and means
"read your own checkout". The values are forge's and win over an `env_vars`
entry of the same name.

A build bakes them into whatever reports its identity — an About page, a
`version.json`, an error reporter's release name. reliant-web shipped
"version unknown" to production before they existed.

## Local iteration: `.forge/source-overrides.yaml`

Pinning without an escape hatch would force every one-line frontend edit
through a commit, a push, a tag and a re-fetch. That is how a feature gets
resented rather than adopted, so a source can be overridden to a local
working copy:

```yaml
# .forge/source-overrides.yaml
sources:
  github.com/reliant-labs/reliant: ../reliant
```

Paths are relative to the project root (or absolute). The map is keyed by
**repo**, not by component, so one entry covers every component sourced
from that repository — and it survives a ref bump, which a repo+ref key
would not.

Three properties make this safe:

- **It is explicit.** forge never auto-adopts a sibling checkout it happens
  to find on disk. Silent adoption would reintroduce the unpinned build
  invisibly, on the one machine — a maintainer's laptop — least likely to
  notice.
- **It cannot be committed.** `.forge/*` is gitignored, so an override
  cannot follow a change into CI. The pin is what builds there, always.
- **It is reported.** Every overridden resolution prints
  `(local override — NOT the pinned ref)`. A build that quietly stopped
  honoring its pin is the exact failure this feature removes.

A stale override — one pointing at a directory that no longer exists — is a
hard error naming the file, not a silent fall back to the pin. Falling back
would build something other than what the developer believes they are
building.

## What is covered today

**Frontends**, end to end: schema (KCL), resolver, cache, overrides, and
build, deploy, and `env up`.

**Shell builds** (the successor to `External.cwd`) declare a pin instead of
requiring a sibling checkout:

```python
build = forge.ShellBuild {
    source = forge.GitSource {repo = "github.com/acme/api", ref = "v1.2.3", subdir = "server"}
    cmd = "make image"
}
```

`source` and `cwd` are mutually exclusive. The command runs in the resolved
subdirectory; fetch failures are errors, never a fallback to the project
root. The same resolution feeds build plans and the release-source check.

**Named Docker contexts** accept either a path/BuildKit reference string or
`GitSource`, in per-workload KCL and in the project-image defaults:

```python
build = forge.DockerBuild {
    build_contexts = {
        lib = forge.GitSource {repo = "github.com/acme/lib", ref = "v2.0.0", subdir = "src"}
        base = "docker-image://alpine:3.22"
    }
}
```

```yaml
docker:
  build_contexts:
    lib:
      repo: github.com/acme/lib
      ref: v2.0.0
      subdir: src
```

Both use the same cache and explicit local overrides as frontends. Per-workload
contexts still replace (not merge with) the project defaults. Resolving code
never rewrites a declaration into a machine-local path. `forge doctor --verbose`
reports pins versus filesystem/BuildKit references across environments without
fetching any sources.

The Forge KCL module itself is supplied by the rendering binary; projects no
longer need a sibling `forge/kcl` dependency in `kcl.mod`.

### A release holds a filesystem sibling checkout to its pin

A `ShellBuild` whose `cwd` is a sibling checkout still builds whatever that
checkout holds. So a release refuses to cut while it is not at the commit
the project pins:

- every `ShellBuild` records the checkout it ran in (HEAD, dirty flag,
  origin, module) in `.forge/state/build-<env>-<service>.json`, and a
  release carries it per artifact as `built_from`;
- `forge env build <env> --release <v>` (and `--plan`, and a deploy that
  re-uses a release) refuses BEFORE building when a sibling checkout is
  dirty, or its HEAD is not the commit pinned by the project's `go.mod`
  require of the module the checkout declares, or by a `forge.GitSource`
  naming its repository;
- `--release --no-build` applies the same rule to the checkout the earlier
  build RECORDED.

The refusal names the checkout, every pin, and the `git -C <dir> checkout
--detach <commit>` that fixes it. A sibling nothing pins is noted, not
refused. The project's own checkout is the release's own provenance and is
not judged here.

## Compatibility

This is purely additive. A project that declares no `source:` anywhere
constructs no resolver, touches no cache, and renders byte-identically —
the resolution pass returns immediately when nothing needs it. The KCL
render emits `source: null` for a path-declared frontend and the Go struct
omits the field entirely when absent.
