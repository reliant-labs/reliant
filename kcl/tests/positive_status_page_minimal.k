"""
Positive test — a status page with only a title and a component renders no
optional key, and an env without `status_page` renders no `status_page` key,
so its shape encodes exactly as it did before the field.
"""

import forge
import forge.workloads as fw

_frontends = [forge.Frontend {
    name = "site"
    image = "ghcr.io/acme/site"
    path = "site"
    type = "vite"
    public_dir = "dist"
    runtime = forge.OnHosted {}
}]

_with = forge.render(forge.Bundle {
    project = "fixture"
    control_plane = forge.ControlPlane {}
    frontends = _frontends
    status_page = forge.StatusPage {
        title = "Status"
        components = [forge.StatusComponent {name = "Website", checks = ["site"]}]
    }
})

_without = forge.render(forge.Bundle {
    project = "fixture"
    control_plane = forge.ControlPlane {}
    frontends = _frontends
})

assert_minimal = _with.status_page == {title = "Status", components = [{name = "Website", checks = ["site"]}]}
assert_absent = "status_page" not in _without
