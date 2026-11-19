"""
reliant's `desktop` environment: where the desktop app's renderer source maps
are filed. It deploys nothing.

The desktop app is built and published by .github/workflows/release.yml, not
by forge, so no `forge env build` ever sees its renderer (frontends/web/dist).
Its browser errors report into tenant zero's self-managed prod env as frontend
`desktop` (control-plane deploy/kcl/lib/env.k, reliant_desktop_ingest_keys),
and the platform symbolicates them only if that build's maps are in the
registry. The release workflow files them with

    task release:sourcemaps DIST=<dist> BUILD_ID=<version without the v>

which runs `forge ui sourcemaps push ... --env desktop`. This env exists for
that one command: it names the control plane (Reliant cloud, the registry the
maps go to) and the credential's variable. The org and project the maps are
filed under are the env the bundle REPORTS into, not this one, so the task
passes them (--org, --project): a sourcemaps:write credential cannot ask the
control plane which org it acts for (reliant#2535).

    forge env render desktop            # 0 objects: nothing to deploy

Never add a workload or frontend here.
"""

import forge

output = forge.render(forge.Bundle {
    project = "reliant"
    env = "desktop"
    # Reliant cloud. The credential is a registry-only org token carrying only
    # sourcemaps:write (Settings → API tokens), the release workflow's
    # RELIANT_SOURCEMAPS_TOKEN secret, exported under this name.
    control_plane = forge.ControlPlane {}
    # Hosted: the maps go to the platform registry, not .forge/sourcemaps/.
    observability = forge.Observability {}
})
