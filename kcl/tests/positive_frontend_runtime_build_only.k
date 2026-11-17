"""
Positive test — a frontend bound to forge.BuildOnly: built, never shipped.

The build-only frontend is what a sibling frontend's `bundle` assembles: its
output must exist on disk before the shipping frontend assembles the site.
This used to be the silent meaning of a frontend with no deploy block; it is
now a named runtime, the same forge.BuildOnly a workload binds.
"""

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    frontends = [
        forge.Frontend {name = "spa", type = "vite", path = "spa", runtime = forge.BuildOnly {}}
        forge.Frontend {
            name = "admin-web"
            path = "admin-web"
            base_path = "/admin"
            bundle = [forge.BundleDir {src = "spa/dist"}]
            runtime = forge.OnFirebase {project = "p", site = "s"}
        }
    ]
})

assert_build_only = output.frontends[0].runtime == {"type": "build-only"}
assert_build_only_public_dir = output.frontends[0].public_dir == "dist"
assert_shipper = output.frontends[1].runtime.type == "firebase"
