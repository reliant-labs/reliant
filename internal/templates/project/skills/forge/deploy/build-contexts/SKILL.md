---
name: deploy/build-contexts
description: Docker build contexts for a forge build — the MAIN context a DockerBuild sends (DockerBuild.context), the NAMED --build-context entries a Dockerfile COPY --from=s, and which of the two a given Dockerfile actually needs.
---

# Docker Build Contexts

Two different things share the word "context", and picking the wrong one
produces a build failure that names neither.

| | what it is | where it is declared |
|---|---|---|
| **MAIN context** | the ONE directory docker sends as `.` — what every un-prefixed `COPY` resolves against | `DockerBuild.context` on the workload |
| **NAMED contexts** | extra sources a `COPY --from=<name>` / `FROM <name>` pulls from | `forge.yaml` `docker.build_contexts`, or `DockerBuild.build_contexts` |

## The MAIN context (`DockerBuild.context`)

A DockerBuild sends the PROJECT ROOT as its context unless the workload says
otherwise. `context` is a directory relative to the project root:

```kcl
fw.Workload {
    name = "internal-console"
    build = forge.DockerBuild {
        dockerfile = "frontends/internal-console/Dockerfile"
        context    = "frontends/internal-console"
    }
}
```

**Set it when the Dockerfile was written to be run from its own directory** —
the giveaway is an un-prefixed copy of a file that is not at the repo root:

```dockerfile
WORKDIR /app/frontends/internal-console
COPY package.json package-lock.json* ./    # ← resolves against the CONTEXT
```

Under the default root context that same line fails with

```
ERROR: failed to compute cache key: "/package.json": not found
```

which names the Dockerfile, not the context, and so reads as a Dockerfile
bug. A Dockerfile that COPYs repo-root-relative paths
(`COPY frontends/internal-console/package.json ./`) wants the default and
should not set `context` at all.

Two details that trip people up:

- **`dockerfile` is always project-root relative**, whatever `context` says,
  and it need NOT live under the context. Docker allows `-f` outside the
  context and forge does not narrow that.
- **A narrower context is not just correctness, it is speed** — docker sends
  the context to the daemon, so a root context on a large monorepo ships the
  whole tree for a build that reads one directory.

forge refuses a `context` that is absolute, escapes the project root, does
not exist, or is a file. `forge build <env> --plan` runs the same check and
prints the resolved context in every docker step
(`docker build -f frontends/internal-console/Dockerfile frontends/internal-console`),
so a context mistake is a PR-time failure rather than a release-cut one.

## NAMED contexts (`build_contexts`)

When a Dockerfile needs files from OUTSIDE the project tree — a sibling
checkout the `go.mod` `replace`s against, a shared-libs monorepo sibling, a
base image to pin — declare each as a named context. Every entry becomes one
`docker buildx --build-context name=value`:

```yaml
# forge.yaml — project-wide
docker:
  build_contexts:
    shared: ../shared-libs             # relative path, resolved against forge.yaml's dir
    base: docker-image://acme/base:v3  # registry image — pin or local-override a FROM
```

Consume them via `COPY --from=shared` or `FROM base`.

A single workload can declare its own instead, which is the better default
once more than one image exists — each Dockerfile then names ONLY what it
actually consumes:

```kcl
forge.DockerBuild {
    dockerfile = "Dockerfile"
    build_contexts = {forge = "../forge", reliant = "../reliant"}
}
```

`DockerBuild.build_contexts` REPLACE the project-level ones for that
workload rather than merging with them, so a workload that declares any
declares all of the ones its Dockerfile needs. Unset, it inherits
`forge.yaml`'s.

Named contexts cannot substitute for the main one: a `COPY package.json ./`
reads the MAIN context, and no `--build-context` entry changes where that
looks.

## Base images

forge is base-image-AGNOSTIC. It does not discover, mirror, pin, or inject
base images, and the only `--build-arg`s on a build are the workload's
explicit `build_args`. A Dockerfile's `FROM` lines are the whole story — pin
them with `FROM …@sha256:…` if reproducibility matters.
