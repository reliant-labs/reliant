"""
reliant's `release` environment: what reliant BUILDS and ships but never runs,
declared through forge's generic build primitives. It deploys nothing.

    forge env render release                                  # 0 objects: build-only
    forge env build release --plan                            # preflight every artifact
    forge env build release --target reliant-image --docker   # the server image, locally

WHAT IS HERE. Each artifact is a `kind = "tool"` workload on `forge.BuildOnly`,
declared in deploy/kcl/workloads.k:

  * reliant-image — the production server image: the root Dockerfile, as a
    DockerBuild.
  * reliant-cli — the standalone CLI, cross-compiled for goreleaser's five
    platforms as GoBuild variants: bin/reliant-cli-<goos>-<goarch>[.exe].
    Built by `forge env build release --target reliant-cli`; goreleaser and
    release.yml's build-backend still build what ships.

WHAT SHIPS TODAY IS UNCHANGED. This env declares the builds; it publishes
nothing on its own and no workflow calls it yet. The server image prod runs is
still built by control-plane's release (rc-train, scripts/build-reliant-image.sh)
and by .github/workflows/build-images.yml on a tag. The image name below is the
one build-images.yml pushes to, so a `--push` from here lands where reliant's
own image already goes.

THE VERSION STAMP. A release build stamps internal/version from
scripts/release-identity.sh, which KCL cannot run, so the values are render
options. Unset, each takes the Dockerfile's own ARG default, so a plain
`forge env build release` builds exactly what a plain `docker build .` does:

    forge env build release --target reliant-image --docker \\
      -D stamp_version="$(bash scripts/release-identity.sh --commit HEAD)" \\
      -D stamp_commit="$(git rev-parse HEAD)" \\
      -D stamp_commit_time="$(bash scripts/release-identity.sh --commit-time --commit HEAD)"
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

# The standalone CLI, stamped like the image.
_cli = wl.reliant_cli | {
    runtime = forge.BuildOnly {build_variants = wl.reliant_cli_variants(_stamp)}
}

output = forge.render(forge.Bundle {
    project = "reliant"
    env = "release"
    workloads = [
        _server_image
        _cli
    ]
})
