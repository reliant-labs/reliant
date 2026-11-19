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

## Profile-guided builds

A Go binary with a committed `cmd/<bin>/default.pgo` is built with it (Go's default `-pgo=auto`), on every runtime. `forge pgo pull --env prod` writes the file from production's CPU profiles and `forge env build` prints a `pgo:` line per binary; `pgo = forge.PGO {...}` on a `GoBuild` (or `DockerBuild.pgo`, keyed by binary) sets the source env and window, and `enabled = False` passes `-pgo=off`. Load `observability/profiles`.

## Skip an unchanged ShellBuild with `inputs`

A `forge.ShellBuild` runs its `cmd` on every build, because forge cannot see
what a shell command reads. Declare it, and forge skips the command when
nothing it depends on moved:

```
build = forge.ShellBuild {
    cwd = "../api"
    cmd = "docker build -t ${_ref} . && docker push ${_ref}"
    inputs = ["go.mod", "go.sum", "**/*.go", "Dockerfile", "!**/*_test.go"]
}
```

Globs are relative to `cwd` (`../` reaches a sibling; `!` excludes). They
may reach only into the directory holding the project, where its sibling
checkouts live; a pattern or symlink that leads further is refused and the
build runs. It skips
only when the content fingerprint of the inputs, `cmd`, `env`, platform and
destination match the last SUCCESSFUL build in this env, and the registry
still serves that build's digest at the tag; deploy then pins that digest.
`--rebuild` (on `forge build`, `forge env build`, `forge env up`) forces a
run. An image-less build and an input glob that matches nothing always run.
Keep the command's outputs out of its inputs; forge warns when a build
rewrites its own inputs.

**Releases reuse the previous release's image.** `forge env build <env>
--release <v>` compares the same inputs, with `cmd`/`env` from a render
whose `image_tag` is a sentinel (so only the tag the KCL composed in is left
out; the cwd is keyed relative to the project), against the `inputs_digest` the image
recorded in the release the env runs now, then the project's newest release.
Equal, and the registry still serves that digest: forge points the new tag
at it and records `carried_from`, without running the command, so a build
that is not reproducible no longer rolls every workload pinned to it. The log
names the matched inputs and the reused digest, or why it ran (no inputs,
no earlier release, a release cut before forge recorded inputs, inputs
changed, digest gone). A command that bakes the tag into the image (a
version stamp) would carry the old stamp: give it no `inputs`. A hosted
ledger keeps `inputs_digest` only once its control plane stores it; until
then hosted releases rebuild as before.

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
