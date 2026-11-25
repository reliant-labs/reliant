"""
Positive test — which binary a DockerBuild workload runs, for PGO.

Two sides apply the same rule and must agree: the render
(kcl/render.k _pgo_docker_binary) puts FORGE_PROFILES_PGO=off on a workload
whose binary has PGO off, and the build (internal/cli dockerWorkloadBinary)
passes FORGE_PGO_<BIN>=off for it. internal/cli
TestPGODockerBinaryAgreesWithKCL renders THIS file and checks the build
resolves the same workloads to the same disabled binary, so the two cannot
drift apart.

  * the project Dockerfile, however its path and the root context are
    spelled ("./Dockerfile", context "./", or the defaults);
  * every command form the scaffolded image runs a binary by: /app/<bin>,
    /app/bin/<bin>, and the relative bin/<bin> (WORKDIR is /app);
  * with no command, the project Dockerfile runs ./cmd/<project>;
  * a command that names no image binary runs none forge can see;
  * another Dockerfile runs a binary only when its pgo names it.
"""

import forge
import forge.workloads as fw

_on_cluster = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}}
_off = forge.PGO {enabled = False}
_root = forge.DockerBuild {context = "./", dockerfile = "./Dockerfile", pgo = {"./cmd/app" = _off}}
_other = forge.DockerBuild {context = "./", dockerfile = "svc/Dockerfile", pgo = {"./cmd/app" = _off, "./cmd/svc" = forge.PGO {}}}

output = forge.render(forge.Bundle {
    project = "shop"
    workloads = [
        fw.Workload {name = "rel-bin", image = "localhost:5050/shop", build = _root, command = ["bin/app"], runtime = _on_cluster}
        fw.Workload {name = "app-bin", image = "localhost:5050/shop", build = _root, command = ["/app/bin/app"], runtime = _on_cluster}
        fw.Workload {name = "app-root", image = "localhost:5050/shop", build = _root, command = ["/app/app"], runtime = _on_cluster}
        fw.Workload {name = "primary", image = "localhost:5050/shop", build = _root, runtime = _on_cluster}
        fw.Workload {name = "foreign-cmd", image = "localhost:5050/shop", build = _root, command = ["/usr/local/bin/app"], runtime = _on_cluster}
        fw.Workload {name = "nested", image = "localhost:5050/shop", build = _root, command = ["/app/bin/x/app"], runtime = _on_cluster}
        fw.Workload {
            name = "default-paths"
            image = "localhost:5050/shop-default"
            build = forge.DockerBuild {pgo = {"./cmd/app" = _off}}
            command = ["/app/app"]
            runtime = _on_cluster
        }
        fw.Workload {name = "other-df", image = "localhost:5050/svc", build = _other, command = ["bin/app"], runtime = _on_cluster}
        fw.Workload {name = "other-df-unnamed", image = "localhost:5050/svc", build = _other, command = ["/app/edge"], runtime = _on_cluster}
    ]
})

_envof = lambda n: str -> {str:} {
    {e.name: e.value for e in ([w for w in output.workloads if w.name == n][0].spec.env or []) if "value" in e}
}
_off_names = sorted([w.name for w in output.workloads if _envof(w.name)["FORGE_PROFILES_PGO"] == "off"])

assert_project_dockerfile_command_forms = all n in ["rel-bin", "app-bin", "app-root", "default-paths"] {n in _off_names}
assert_other_dockerfile_named_binary = "other-df" in _off_names
assert_exactly_these_off = _off_names == ["app-bin", "app-root", "default-paths", "other-df", "rel-bin"]
