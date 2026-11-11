"""
reliant's `demo` environment: demo.reliantlabs.io, the public forge demo.

DEPRECATED (2026-11-10). The demo now ships with prod: control-plane's prod env
declares the same `forge-demo` build from reliant-web's pinned source
(control-plane deploy/kcl/lib/reliant_web.k), so every release promotes it by
digest. This env keeps working, as the domain's rollback target, until
prod has served demo.reliantlabs.io through a release; then it is retired
(dev-docs/forge-demo-cutover.md, "Into prod"). Keep the two declarations'
build (WEB_BUILD_TARGET, public_dir) and runtime keys alike until then.

ONE HOSTED STATIC SITE AND NOTHING ELSE. `forge-demo` is a second build of
frontends/web (vite.config.ts's demo target: entry demo.html →
src/demoBundle/main.tsx), containing the forge console's pages, the demo's mock
transport and sample data, the tour and "How this app was built", and none of
Reliant's chat, onboarding, sign-in, Supabase, daemon or settings code
(vite-plugin-demo-bundle.ts fails the build if any of it arrives). It calls no
backend: every RPC is answered in the browser.

    forge env render demo                        # 0 cluster objects; hosted: forge-demo (static)
    forge env build demo --release <v>           # build once (npm run build with WEB_BUILD_TARGET=demo), push, record
    forge env deploy demo <v>                    # publish; the platform writes config.js
    forge obs check demo --wait --json

WHY IT WAS HERE, AND NOT IN control-plane's prod (superseded above). The demo is built from THIS
checkout (`path`, not a GitSource pin), so it ships whenever reliant main
moves, on its own schedule: no pin bump in control-plane, none of prod's
release train, and no coupling to the API it does not call. A second reliant
pin in control-plane would have to move in lockstep with go.mod's (that repo's
pin-coherence checks), which is exactly the coupling a demo must not have.

THE DOMAIN is a control-plane resource bound to this env's frontend, not spec
(`forge domain bind demo.reliantlabs.io --env demo --target forge-demo`). The cutover
from control-plane's prod/reliant-web, its checks and its rollback:
dev-docs/forge-demo-cutover.md.
"""

import forge

# What the bundle reads at RUNTIME (window.__FORGE_CONFIG__, src/lib/config_gen.ts).
# No API URL, no auth mode, no keys: the demo has no backend and no sign-in.
_runtime_config = {
    # Where the console's reads come from: the built-in sample data. "remote"
    # plus DEMO_DATA_URL reads a public demo-data gateway first, falling back
    # to the sample data (Forge/Demo/demoData/demoTransport.ts).
    DEMO_DATA_SOURCE = "fixtures"
    DEMO_DATA_URL = ""
    # Browser telemetry to the platform's same-origin ingest for this site,
    # filed under service reliant-forge-demo, frontend forge-demo (lib/telemetry.ts). What it records about
    # an anonymous visitor is DEMO_TELEMETRY's ("replay": page and tour events
    # and masked session replay; "analytics": events only; "off"); a browser
    # sending Do Not Track or Global Privacy Control is never recorded.
    OTEL_ENDPOINT = "/_otel"
    DEMO_TELEMETRY = "replay"
}

_forge_demo = forge.Frontend {
    name = "forge-demo"
    type = "vite"
    path = "frontends/web"
    # The lockfile is pnpm's (pnpm-lock.yaml).
    dev_runner = "pnpm"
    # forge builds every frontend with `npm run build` in its directory and
    # its env_vars as the build's environment: this one selects the demo
    # target, which builds demo.html into dist-demo/ as its index.html.
    env_vars = [forge.EnvVar {name = "WEB_BUILD_TARGET", value = "demo"}]
    public_dir = "dist-demo"
    asset_dir = "assets"
    image = "forge-demo"
    runtime_config = _runtime_config
    telemetry = forge.BrowserTelemetry {replay = True}
    runtime = forge.OnHosted {}
}

output = forge.render(forge.Bundle {
    project = "reliant"
    env = "demo"
    # Reliant cloud: needed by OnHosted.
    control_plane = forge.ControlPlane {}
    # The platform keeps replay events only for an env that opts in.
    observability = forge.Observability {}
    frontends = [_forge_demo]
})
