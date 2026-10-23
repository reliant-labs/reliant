"""
Positive test — a frontend bound to forge.OnFirebase.

The build and the assembly are the frontend's (public_dir, base_path,
bundle); the runtime names the Firebase project/site, the SPA's entry
document and how many releases' hashed assets stay serveable. `target`
omitted projects as null (the consumer defaults it to the site). Mirrors
the reliant-web + admin-web recipe: a Next.js export under /admin with a
sibling Vite SPA bundled at the site root, whose fallback is /index.html.
Declared rewrites (a Cloud Run API route) still pass through verbatim.
"""

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    frontends = [forge.Frontend {
        name = "admin-web"
        type = "nextjs"
        path = "admin-web"
        public_dir = "out"
        base_path = "/admin"
        bundle = [forge.BundleDir {src = "../reliant-web/dist"}]
        runtime = forge.OnFirebase {
            project = "reliant-nonprod-490701"
            site = "reliant-staging"
            spa_fallback = "/index.html"
            rewrites = [{source = "/api/**", run = {serviceId = "api", region = "us-central1"}}]
        }
    }, forge.Frontend {
        # Firebase honours cache_control (rendered as firebase.json headers),
        # and a Vite build's asset dir defaults to "assets".
        name = "web"
        type = "vite"
        path = "web"
        cache_control = [forge.CacheRule {pattern = "assets/**", cache_control = "public, max-age=600"}]
        runtime = forge.OnFirebase {project = "p", site = "web", keep_asset_releases = 3}
    }, forge.Frontend {
        name = "moved"
        type = "vite"
        path = "moved"
        asset_dir = "static/hashed"
        runtime = forge.OnFirebase {project = "p", site = "moved"}
    }]
})

_f = output.frontends[0]
assert_type = _f.runtime.type == "firebase"
assert_project = _f.runtime.project == "reliant-nonprod-490701"
assert_site = _f.runtime.site == "reliant-staging"
assert_target_null = _f.runtime.target == None
assert_spa_fallback = _f.runtime.spa_fallback == "/index.html"
assert_rewrites = _f.runtime.rewrites[0].source == "/api/**"
assert_keep_default = _f.runtime.keep_asset_releases == 10
assert_next_asset_dir = _f.asset_dir == "_next/static"
assert_public_dir = _f.public_dir == "out"
assert_base_path = _f.base_path == "/admin"
assert_bundle = _f.bundle[0].src == "../reliant-web/dist"

_w = output.frontends[1]
assert_vite_asset_dir = _w.asset_dir == "assets"
assert_cache_control = _w.cache_control[0].pattern == "assets/**"
assert_keep = _w.runtime.keep_asset_releases == 3
assert_no_fallback = _w.runtime.spa_fallback == None

assert_declared_asset_dir = output.frontends[2].asset_dir == "static/hashed"
