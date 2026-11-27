"""
Positive test — forge.FrontendURL in a frontend's env_vars, the build-time
origin a static site writes into its HTML (canonical, og:url, robots.txt).

Lowered by where the TARGET runs: a dev server's URL is known at render, so
it becomes the value; a hosted frontend's hostname is the control plane's, so
the reference is kept (value null) for the build to resolve from the env's
live status. A var with no FrontendURL renders as before.
"""

import forge

_dev = forge.render(forge.Bundle {
    project = "fixture"
    frontends = [forge.Frontend {
        name = "web"
        path = "."
        type = "vite"
        port = 5174
        base_path = "/docs"
        env_vars = [
            forge.EnvVar {name = "SITE_URL", frontend_url = forge.FrontendURL {frontend = "web"}}
            forge.EnvVar {name = "VITE_FLAG", value = "on"}
        ]
        runtime = forge.OnHost {}
    }]
})

_hosted = forge.render(forge.Bundle {
    project = "fixture"
    env = "prod"
    control_plane = forge.ControlPlane {endpoint = "https://cp.example"}
    frontends = [forge.Frontend {
        name = "web"
        image = "web"
        path = "."
        type = "nextjs"
        env_vars = [forge.EnvVar {name = "SITE_URL", frontend_url = forge.FrontendURL {frontend = "web"}}]
        runtime = forge.OnHosted {}
    }]
})

# Your own bucket's build may take its origin from a hosted sibling: the
# reference is kept, and `forge env deploy` resolves it before the build.
_bucket = forge.render(forge.Bundle {
    project = "fixture"
    env = "prod"
    control_plane = forge.ControlPlane {endpoint = "https://cp.example"}
    frontends = [
        forge.Frontend {name = "site", image = "site", path = "site", type = "vite", runtime = forge.OnHosted {}}
        forge.Frontend {
            name = "docs"
            path = "docs"
            type = "vite"
            env_vars = [forge.EnvVar {name = "SITE_URL", frontend_url = forge.FrontendURL {frontend = "site"}}]
            runtime = forge.OnBucket {bucket = "gs://acme-docs"}
        }
    ]
})
_bucket_var = [f for f in _bucket.frontends if f.name == "docs"][0].env_vars[0]
assert_bucket_ref_kept = _bucket_var.frontend_url == {"frontend": "site"} and _bucket_var.value == None

_dev_vars = _dev.frontends[0].env_vars
assert_dev_resolved = _dev_vars[0].name == "SITE_URL" and _dev_vars[0].value == "http://localhost:5174/docs"
assert_dev_no_ref = "frontend_url" not in _dev_vars[0]
assert_literal_unchanged = _dev_vars[1].value == "on" and "frontend_url" not in _dev_vars[1]

_hosted_var = _hosted.frontends[0].env_vars[0]
assert_hosted_ref_kept = _hosted_var.frontend_url == {"frontend": "web"}
assert_hosted_no_value = _hosted_var.value == None
