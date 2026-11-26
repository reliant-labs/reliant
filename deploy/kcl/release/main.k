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
  * reliant-cli — the standalone CLI, cross-compiled for goreleaser's five
    platforms as GoBuild variants: bin/reliant-cli-<goos>-<goarch>[.exe].
    Built by `forge env build release --target reliant-cli`; goreleaser and
    release.yml's build-backend still build what ships.
  * reliant-desktop — the packaged desktop app for this machine's platform,
    unpublished: electron/scripts/pack-desktop.js, the electron-builder call
    release.yml's desktop jobs run. Its prerequisites (backend binaries, the
    renderer, the build config) are listed beside it in workloads.k.

WHAT SHIPS TODAY IS UNCHANGED. This env declares the builds; it publishes
nothing on its own and no workflow calls it yet. The server image prod runs is
still built by control-plane's release (rc-train, scripts/build-reliant-image.sh)
and by .github/workflows/build-images.yml on a tag. The image name below is the
one build-images.yml pushes to, so a `--push` from here lands where reliant's
own image already goes.

THE VERSION STAMP. A release build stamps internal/version from
scripts/release-identity.sh, which KCL cannot run, so the values are render
options: each becomes one of the Dockerfile's build args. Unset, each takes
the Dockerfile's own ARG default, the value a plain `docker build .` stamps:

    forge env build release --target reliant-image --release <v> \\
      -D stamp_version="$(bash scripts/release-identity.sh --commit HEAD)" \\
      -D stamp_commit="$(git rev-parse HEAD)" \\
      -D stamp_branch="$(git rev-parse --abbrev-ref HEAD)" \\
      -D stamp_commit_time="$(bash scripts/release-identity.sh --commit-time --commit HEAD)"

Every stamp is a function of the commit, never of the clock: BUILD_DATE
(internal/version.Date, what `reliant version` prints as "built") is the
commit time as RFC 3339 UTC, derived from stamp_commit_time, so it agrees with
the build number and with SOURCE_DATE_EPOCH. That is what lets a second
release of an unchanged commit reuse the first one's image (the image's
`inputs`, in workloads.k): forge keys the reuse on the build args, so a
wall-clock date would rebuild on every cut. stamp_date still overrides it, but
any value that differs per build defeats the reuse.

Given the same five values, the binary is byte for byte the one a plain
`docker build --build-arg ...` of the same tree compiles, and on a shared
layer cache so is every layer. The image config differs only in its
timestamps: forge stamps them with SOURCE_DATE_EPOCH (the last commit touching
the build's inputs) and passes --provenance=false. A cold rebuild still moves
the digest, because the apt layer is not reproducible (ldconfig's cache and
apt's logs differ per run), whoever runs the build.
"""

import forge
import ..workloads as wl
import ..dev_sign_in as dsi

# ── The version stamp (render options) ──────────────────────────────────────
# Defaults are the Dockerfile's ARG defaults.
_commit_time = option("stamp_commit_time", type="str", default="0", help="internal/version.CommitTime: release-identity.sh --commit-time (0 = unordered); also sets internal/version.Date")
_date = option("stamp_date", type="str", default="", help="internal/version.Date override, RFC 3339 UTC (default: the commit time, from stamp_commit_time; a per-build value defeats image reuse)")
_stamp = {
    VERSION = option("stamp_version", type="str", default="dev", help="internal/version.Version: scripts/release-identity.sh's output (default: the Dockerfile's \"dev\")")
    COMMIT = option("stamp_commit", type="str", default="unknown", help="internal/version.Commit: the full commit sha")
    BUILD_DATE = _date or wl.commit_date(_commit_time)
    BRANCH = option("stamp_branch", type="str", default="unknown", help="internal/version.Branch: the ref the build is from")
    COMMIT_TIME = _commit_time
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

output = forge.render(dsi.only_in_dev("release", forge.Bundle {
    project = "reliant"
    env = "release"
    workloads = [
        _server_image
        _cli
        wl.desktop_app
    ]
    # Every release cut records an SBOM per artifact and judges it: a KEV or
    # fixable critical finding refuses the cut (`vuln_gate (release,
    # enforce)`). The gate needs the SBOMs, so provenance_sbom is enforced too.
    # This env has no control_plane; only the signing verifiers need one.
    delivery_policy = forge.DeliveryPolicy {
        vuln_gate = forge.VulnPolicy {mode = "enforce"}
        provenance_sbom = forge.Control {mode = "enforce"}
    }
}))
