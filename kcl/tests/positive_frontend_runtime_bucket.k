"""
Positive test — a frontend bound to forge.OnBucket: YOUR bucket.

The static build facts (public_dir, base_path, bundle, the ordered
cache_control rules) project on the FRONTEND; the runtime carries only where
the tree goes: the bucket, the CDN with its invalidation policy, and
keep_releases. A Next.js static export mounted under /admin with a sibling
Vite SPA bundled at the site root, immutable caching on content-hashed
assets and must-revalidate on everything else.

A second, MINIMAL frontend pins the defaults: keep_releases = 0 survives as 0
("retain everything" — read back as the default 10 it would prune a bucket
the author asked forge to leave alone), an absent cdn is null, and a bare
bucket name passes verbatim.
"""

import forge

_full = forge.Frontend {
    name = "admin-web"
    type = "nextjs"
    path = "admin-web"
    env_vars = [forge.EnvVar {name = "NEXT_PUBLIC_API_URL", value = "https://api.staging.example.com"}]
    public_dir = "out"
    base_path = "/admin"
    bundle = [forge.BundleDir {src = "../reliant-web/dist", dest = ""}]
    cache_control = [
        forge.CacheRule {pattern = "_next/static/**", cache_control = "public, max-age=31536000, immutable"}
        forge.CacheRule {pattern = "**", cache_control = "public, max-age=0, must-revalidate"}
    ]
    runtime = forge.OnBucket {
        bucket = "gs://reliant-staging-web"
        cdn = forge.StaticSiteCDN {url_map = "reliant-staging-lb", extra_invalidate_paths = ["/manifest.json"]}
        keep_releases = 5
    }
}

_min = forge.Frontend {
    name = "web"
    type = "vite"
    path = "web"
    runtime = forge.OnBucket {bucket = "reliant-preview-web", keep_releases = 0}
}

output = forge.render(forge.Bundle {project = "fixture", frontends = [_full, _min]})

_f = output.frontends[0]
_r = _f.runtime
assert_runtime_type = _r.type == "bucket"
assert_bucket = _r.bucket == "gs://reliant-staging-web"
assert_cdn_url_map = _r.cdn.url_map == "reliant-staging-lb"
assert_cdn_invalidate_default = _r.cdn.invalidate == "entrypoints"
assert_cdn_extra_paths = _r.cdn.extra_invalidate_paths[0] == "/manifest.json"
assert_keep_releases = _r.keep_releases == 5
assert_public_dir = _f.public_dir == "out"
assert_base_path = _f.base_path == "/admin"
assert_bundle = len(_f.bundle) == 1 and _f.bundle[0].src == "../reliant-web/dist" and _f.bundle[0].dest == ""
# DECLARATION ORDER: first match wins at upload time.
assert_cache_order = [c.pattern for c in _f.cache_control] == ["_next/static/**", "**"]
assert_envvar = _f.env_vars[0].value == "https://api.staging.example.com"

_m = output.frontends[1]
assert_min_bucket_bare = _m.runtime.bucket == "reliant-preview-web"
assert_min_keep_zero_survives = _m.runtime.keep_releases == 0
assert_min_cdn_null = _m.runtime.cdn == None
assert_min_public_dir_default = _m.public_dir == "dist"
assert_min_base_path_null = _m.base_path == None
# Not hosted: references resolve here, and no control plane is needed.
assert_no_control_plane = "control_plane" not in output
