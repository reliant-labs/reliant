"""
reliant's `preview` environment: a hosted, clickable preview of the web app,
signed in, on the shop world's sample data, reaching no backend.

THE BYTES ARE THE PRODUCTION BUILD. `web` is frontends/web built by the plain
`npm run build` (no env_vars, no build target, no harness plugin), the same
dist/ app.reliantlabs.io serves. Only its config.js differs: it names a
fixture scenario, so the build runs in fixture mode
(frontends/web/src/lib/fixturesConfig.ts). The page then lazily loads
src/fixtures/install.ts, which answers every Connect RPC in the page from the
shop pack (src/fixtures/shop/app.ts), behind the app's real interceptors, and
refuses every write. Its fetch boundary (src/fixtures/outside.ts) answers the
Supabase sign-in with a fixed session for the pack's user and throws on any
other request off the page. The visual harness (frontends/web/e2e/visual)
renders the same build on the same pack, so a screenshot there is what a
preview visitor sees, at a fixed clock.

Only this env may turn fixture mode on: internal/forgetest/fixtures_env_test.go
fails a fixture key in any other env's source or render, and checks that this
one renders them.

    forge env render preview                     # 0 cluster objects; hosted: web (static)
    forge env build preview --release <v>        # build once (npm run build), push, record
    forge env deploy preview <v>                 # publish; the platform writes config.js
    forge obs check preview --wait --json

`fixtures = forge.Fixtures` needs a forge with forge#2719; until reliant's
pin includes it, this env does not render.

WHERE IT IS HOSTED. The owner's decision: the preview is a frontend in
control-plane's prod beside reliant-web and forge-demo, built from the pinned
reliant and promoted by digest with each prod release. This env is the
declaration that frontend copies, the subject of the render tests, and a local
target. It is not deployed from here. See dev-docs/preview-env.md.
"""

import forge
import frontend_config_gen as fcg
import ..dev_sign_in as dsi
import ..fixtures_guard as fg

# What config.js carries besides the fixture switch (on the frontend below):
# the app's own keys, typed by its schema, so AUTH_MODE = "cloud" still has
# to name every origin it needs. Every origin is a fake one under .invalid
# (RFC 6761: it never resolves), so a request that escaped the page's answers
# could reach nothing; outside.ts throws on them first.
_app_config = fcg.reliantWebConfigRuntime(fcg.ReliantWebConfig {
    # The managed deployment's code path: Supabase sessions, the control
    # plane's billing, managed machines and the console, as app.reliantlabs.io.
    auth_mode = "cloud"
    api_url = "https://api.preview.invalid"
    control_plane_api_url = "https://cp.preview.invalid"
    gateway_url = "https://gw.preview.invalid"
    # Answered in the page by outside.ts (the configured origin's /auth/v1/*).
    supabase_url = "https://supabase.preview.invalid"
    supabase_anon_key = "preview-fixtures"
    # No checkout: embedded checkout reports itself unavailable.
    stripe_publishable_key = ""
    # No browser telemetry from sample data.
    otel_endpoint = ""
    otel_replay = "false"
    # A link, but an origin all the same: fixtures_guard.k refuses any live one.
    download_base_url = "https://downloads.preview.invalid"
})

_web = forge.Frontend {
    name = "web"
    type = "vite"
    path = "frontends/web"
    # The lockfile is pnpm's (pnpm-lock.yaml).
    dev_runner = "pnpm"
    # No env_vars: the build is the production one.
    public_dir = "dist"
    asset_dir = "assets"
    # The release artifact's name. forge resolves it under the platform
    # registry, the deploying org and this project, and appends static.v1.
    # It names the frontend, not the env, so another env of this project
    # that serves `web` promotes the same artifact by digest.
    image = "web"
    runtime_config = _app_config
    # The fixture switch: forge renders it into config.js as
    # FIXTURES_SCENARIO = "shop" and FIXTURES_READ_ONLY = "true" (read_only
    # defaults to True; a hosted env never writes). Needs a forge with
    # forge#2719; it also refuses either key in runtime_config beside it.
    fixtures = forge.Fixtures {scenario = "shop"}
    runtime = forge.OnHosted {}
}

# fixtures_guard.k refuses the render if a fixture-mode frontend names a live
# origin: the build still carries the real sign-in and API code.
output = forge.render(dsi.only_in_dev("preview", fg.inert_when_fixtures(forge.Bundle {
    project = "reliant"
    env = "preview"
    # Reliant cloud: needed by OnHosted.
    control_plane = forge.ControlPlane {}
    frontends = [_web]
    # The preview carries the production web build, so it is held to prod's
    # bar: a KEV or fixable critical finding in its SBOM (from pnpm-lock.yaml)
    # refuses the cut. The gate needs the SBOM, so provenance_sbom is enforced
    # too.
    delivery_policy = forge.DeliveryPolicy {
        vuln_gate = forge.VulnPolicy {mode = "enforce"}
        provenance_sbom = forge.Control {mode = "enforce"}
    }
})))
