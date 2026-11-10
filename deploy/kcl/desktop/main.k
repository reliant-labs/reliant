"""
`desktop`: where the packaged desktop app's renderer files its source maps.
Nothing runs here and nothing is deployed from it. It has no workloads,
frontends or infra, so `forge env up desktop` and `forge env deploy desktop`
have nothing to do. It exists for one command, which release.yml's
`renderer-sourcemaps` job runs (scripts/release-sourcemaps.sh):

    forge frontend sourcemaps push frontends/web/dist --frontend desktop \
        --build-id <release version> --env desktop --project control-plane

WHY AN ENV. `forge frontend sourcemaps push` takes the control plane, the
registry and the credential from an env's declaration (`--env`) like every
forge command that talks to a control plane. reliant's only other env is
`dev`, which is this machine's stack and holds no CI credential.

WHAT IT NAMES. Reliant cloud (the default `forge.ControlPlane {}`), with the
credential read from RELIANT_DESKTOP_SOURCEMAPS_TOKEN, a repository secret of
the same name. Observability is hosted by default, so the maps go to the
platform registry as `<org>/control-plane/desktop-sourcemaps:<version>`. The
issues worker looks them up there: the renderer reports `forge.frontend =
desktop` and service.version = the release version (frontends/web/src/lib/
telemetry.ts) into tenant zero's prod env, whose project is control-plane
(control-plane's deploy/kcl/lib/env.k, reliant_desktop_ingest_keys).
"""

import forge

output = forge.render(forge.Bundle {
    project = "reliant"
    env = "desktop"
    control_plane = forge.ControlPlane {
        token_env = "RELIANT_DESKTOP_SOURCEMAPS_TOKEN"
    }
})
