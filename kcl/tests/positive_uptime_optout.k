"""
Positive test — a hosted env opts out of derived uptime checks: wholesale
(`defaults = False`) or one at a time (`skip`). A declared check is never
opted out by either.
"""

import forge
import forge.workloads as fw

_whole = forge.render(forge.Bundle {
    project = "fixture"
    control_plane = forge.ControlPlane {}
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api:v1"
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        runtime = forge.OnHosted {}
    }]
    frontends = [forge.Frontend {
        name = "site"
        image = "ghcr.io/acme/site"
        path = "site"
        type = "vite"
        public_dir = "dist"
        runtime = forge.OnHosted {}
    }]
    uptime = forge.Uptime {defaults = False}
})

_one = forge.render(forge.Bundle {
    project = "fixture"
    control_plane = forge.ControlPlane {}
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api:v1"
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        runtime = forge.OnHosted {}
    }]
    frontends = [forge.Frontend {
        name = "site"
        image = "ghcr.io/acme/site"
        path = "site"
        type = "vite"
        public_dir = "dist"
        runtime = forge.OnHosted {}
    }]
    uptime = forge.Uptime {skip = ["site"]}
})

_declared = forge.render(forge.Bundle {
    project = "fixture"
    control_plane = forge.ControlPlane {}
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api:v1"
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        runtime = forge.OnHosted {}
    }]
    frontends = [forge.Frontend {
        name = "site"
        image = "ghcr.io/acme/site"
        path = "site"
        type = "vite"
        public_dir = "dist"
        runtime = forge.OnHosted {}
    }]
    uptime = forge.Uptime {
        defaults = False
        checks = [forge.UptimeCheck {name = "mine", url = "https://example.com/up"}]
    }
})

output = {whole = _whole, one = _one, declared = _declared}

assert_defaults_false_derives_none = "uptime_checks" not in _whole
assert_skip_drops_one = [c.name for c in _one.uptime_checks] == ["api"]
assert_declared_survives_opt_out = [c.name for c in _declared.uptime_checks] == ["mine"]
assert_declared_is_user_sourced = _declared.uptime_checks[0].source == "user"
