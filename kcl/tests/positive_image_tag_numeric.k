"""
Positive test — a numeric `image_tag` types as `str` (forge-deploy-prod
regression).

`forge env deploy` passes the image tag as `-D image_tag=<tag>`. On prod it
is an all-digit `git describe` value ("3826648"). A BARE `-D
image_tag=3826648` makes KCL coerce it to `int`, and the render fails with
`image_tag: str, got int`. forge passes it QUOTED (strconv.Quote in
cluster.renderDArgs), which types it as `str` — this fixture renders the way
forge does and pins the value reaching the resolved image.
"""

# kcl-args: image_tag="3826648"

import forge
import forge.workloads as fw

_target = forge.ClusterTarget {cluster = "k3d-myapp", namespace = "myapp-prod"}

output = forge.render(forge.Bundle {
    project = "myapp"
    workloads = [fw.Workload {name = "api", image = "localhost:5050/myapp", runtime = forge.OnCluster {target = _target}}]
})

_tag = option("image_tag")
assert_tag_is_str = typeof(_tag) == "str"
assert_tag_value = _tag == "3826648"
assert_output_tag = output.image_tag == "3826648"
assert_resolved_with_numeric_tag = [m for m in output.manifests if m.kind == "Workload"][0].spec.image == "localhost:5050/myapp:3826648"
