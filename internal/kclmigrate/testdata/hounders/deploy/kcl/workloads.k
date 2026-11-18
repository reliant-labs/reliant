"""hounders-shaped workloads: NO workload declares an image.

This is the scaffolded default, and it is why the original migration dropped
the registry and wrote nothing: forge DERIVES the image from the build's
`output_name`, so there is no `image = "…"` line anywhere to complete. Both
workloads below derive the same artifact, `hounders`.
"""

import forge
import forge.workloads as fw

_app_build = forge.GoBuild {cmd = "./cmd/hounders", output_name = "hounders"}

migrate = fw.Workload {
    name = "migrate"
    kind = "job"
    build = _app_build
    args = ["db", "migrate", "up"]
}

membership = fw.Workload {
    name = "membership"
    kind = "service"
    build = _app_build
    args = ["serve"]
    ports = [fw.Port {name = "http", port = 8080, expose = True}]
}
