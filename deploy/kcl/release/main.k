"""
reliant's `release` environment: what reliant BUILDS and ships but never runs,
declared through forge's generic build primitives. It deploys nothing.

    forge env render release                                         # 0 objects: build-only
    forge env build release --target reliant-image --docker --plan   # preflight the image build
    forge env build release --target reliant-image --docker          # the server image, locally

Name the target. An untargeted `forge env build release` also builds
frontends/web (`npm run build`), though this env declares no frontend: at the
forge this repo pins, `env build` starts from the project's frontend inventory
and drops only the frontends an env binds to a host or hosted runtime, so an
env that names none keeps them all. That is a forge defect (reported), not
part of this env.

WHAT IS HERE. Each artifact is a `kind = "tool"` workload on `forge.BuildOnly`,
declared in deploy/kcl/workloads.k:

  * reliant-image — the production server image: the root Dockerfile, as a
    DockerBuild.

WHAT SHIPS TODAY IS UNCHANGED. This env declares the builds; it publishes
nothing on its own and no workflow calls it yet. The server image prod runs is
still built by control-plane's release (rc-train, scripts/build-reliant-image.sh)
and by .github/workflows/build-images.yml on a tag. The image name below is the
one build-images.yml pushes to, so a `--push` from here lands where reliant's
own image already goes.

THE VERSION STAMP. A release build stamps internal/version from
scripts/release-identity.sh, which KCL cannot run, so the values are render
options: each becomes one of the Dockerfile's build args, exactly as
build-images.yml passes them. Unset, each takes the Dockerfile's own ARG
default, the value a plain `docker build .` stamps:

    forge env build release --target reliant-image --docker \\
      -D stamp_version="$(bash scripts/release-identity.sh --commit HEAD)" \\
      -D stamp_commit="$(git rev-parse HEAD)" \\
      -D stamp_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \\
      -D stamp_branch="$(git rev-parse --abbrev-ref HEAD)" \\
      -D stamp_commit_time="$(bash scripts/release-identity.sh --commit-time --commit HEAD)"

Given the same five values, that build's layers are the ones a plain
`docker build --build-arg ...` of the same tree produces (the binary
byte-for-byte). Only the image config's timestamps differ: forge stamps them
with SOURCE_DATE_EPOCH (the last commit touching the build's inputs) and
passes --provenance=false, so a rebuild of the same commit keeps its digest.
"""

import forge
import ..workloads as wl

# ── The version stamp (render options) ──────────────────────────────────────
# Defaults are the Dockerfile's ARG defaults.
_stamp = {
    VERSION = option("stamp_version", type="str", default="dev", help="internal/version.Version: scripts/release-identity.sh's output (default: the Dockerfile's \"dev\")")
    COMMIT = option("stamp_commit", type="str", default="unknown", help="internal/version.Commit: the full commit sha")
    BUILD_DATE = option("stamp_date", type="str", default="unknown", help="internal/version.Date: the build time, RFC 3339 UTC")
    BRANCH = option("stamp_branch", type="str", default="unknown", help="internal/version.Branch: the ref the build is from")
    COMMIT_TIME = option("stamp_commit_time", type="str", default="0", help="internal/version.CommitTime: release-identity.sh --commit-time (0 = unordered)")
}

# ── The artifacts ───────────────────────────────────────────────────────────

# reliant's own published image (.github/workflows/build-images.yml's
# GAR_NONPROD/reliant).
_server_image = wl.server_image | {
    image = "us-central1-docker.pkg.dev/reliant-nonprod-490701/reliant-nonprod/reliant"
    build = wl.server_image_build(_stamp)
}

output = forge.render(forge.Bundle {
    project = "reliant"
    env = "release"
    workloads = [
        _server_image
    ]
})
