"""
Positive test — a frontend bound to forge.OnHost: the dev server.

The SAME forge.OnHost a workload binds (ADR 0002 §6). It projects as
`runtime = {type: "host"}` and nothing else: the dev server is
`<dev_runner> dev` on the frontend's `port`, so none of the workload-only
OnHost fields (runner, listen_ports, ...) reach the contract. The static
build facts still project, from the frontend: `public_dir` defaults by type
(a Next.js static export writes out/), and `base_path` is the dev mount a
forge.WorkloadURL to the dev server carries.
"""

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    frontends = [
        forge.Frontend {name = "web", path = "frontends/web", port = 3100, base_path = "/app", runtime = forge.OnHost {}}
        forge.Frontend {name = "spa", path = "frontends/spa", type = "vite", port = 5173, runtime = forge.OnHost {}}
    ]
})

_web = output.frontends[0]
_spa = output.frontends[1]
assert_runtime_host = _web.runtime == {"type": "host"}
assert_port = _web.port == 3100
assert_public_dir_nextjs_default = _web.public_dir == "out"
assert_public_dir_vite_default = _spa.public_dir == "dist"
assert_base_path = _web.base_path == "/app"
assert_spa_base_path_null = _spa.base_path == None
assert_bundle_empty = len(_web.bundle) == 0
assert_cache_empty = len(_web.cache_control) == 0
# Nothing is hosted, so no control_plane is needed and none is emitted.
assert_no_control_plane = "control_plane" not in output
