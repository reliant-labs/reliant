---
name: deploy/build
description: Before and during the build of an env's artifacts — the pre-flight checks (lint, tests, validate-kcl, doctor), `forge env build <env> --push` and `forge build <env>` with their flags, the one project image and how `args` pick its subcommand, the hosted registry login, the boot config check before a push, and Docker build contexts.
---

# Pre-flight and build

## Pre-flight checks

```
forge lint              # Go + proto + frontend linters; fixes formatting in place locally
forge lint --check      # report only: print the diffs, write nothing (what CI does, by itself)
task test               # full suite
forge ci validate-kcl   # every env renders to something applyable
forge doctor --signal deploy   # probes, resources, Secrets, migrations
```

Capacity: `deploy/hosted-capacity`.

## Build

```
forge build <env>                 # what <env> declares (host workloads need no image)
forge env build <env> --push          # and push each image to the reference its workload declares
forge registry login <env>        # hosted: nothing to pass; -u/--password-stdin for your own
forge registry ref <env>          # <image>@<digest> per image the last build pushed
forge build <env> --plan          # resolve + preflight the build set; build nothing
forge build --tag=<tag>           # override image tag (default: commit SHA)
forge build --debug               # with debug symbols for Delve
```

One project image carries every binary. Its ENTRYPOINT is the binary and
its CMD the default subcommand (`server`), so a workload's `args` select
what the pod runs, the same subcommand the host runtime runs.

## The hosted registry takes your control-plane credential

A hosted env's images go to the PLATFORM registry with your control-plane
`rlat_` — no registry password; `--push` / `forge env deploy` log in themselves.
A bare `image = "api"` resolves under `<registry_host>/<org>/<project>/`.
Credential sources, CI, denied pushes: load `deploy/hosted-registry`.

A non-hosted env declares no registry: each workload — and each hosted
frontend, via `forge.Frontend.image` — names its own.

Every image is built with `docker build`, so building one needs a running Docker daemon.

## The config check runs before the push

`--push` / `--release` (so `forge env deploy`) boot each workload of the
project image with its rendered env under `FORGE_CONFIG_CHECK=1`; a config
error refuses the push. Cross-field rules go in the config message's CEL, not
a constructor. Load `deploy/config-check`.

## Docker build contexts

A `forge.DockerBuild` sends the PROJECT ROOT unless it sets `context`; a
`COPY` that fails with `failed to compute cache key` usually needs it. Files
outside the tree: named `docker.build_contexts`. Load `deploy/build-contexts`.
