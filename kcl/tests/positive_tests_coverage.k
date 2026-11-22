"""
Positive test — test coverage of an env's Go builds (forge.Tests) renders as
declared, and nothing renders where nothing is declared:

  * Bundle.tests renders only when the env declares something, so an env
    that predates it renders exactly as before (forge computes the default:
    it reads forge.yaml's opt-in);
  * a GoBuild's `coverage` renders only when set; a DockerBuild's is keyed by
    binary package path and absent when empty.
"""

import forge
import forge.workloads as fw

_on_host = forge.OnHost {}

output = forge.render(forge.Bundle {
    project = "demo"
    lifecycle = "local"
    tests = forge.Tests {coverage = True, coverage_packages = ["./internal/...", "./pkg/..."]}
    workloads = [
        fw.Workload {name = "plain", build = forge.GoBuild {cmd = "./cmd/demo"}, args = ["a"], runtime = _on_host}
        fw.Workload {name = "off", build = forge.GoBuild {cmd = "./cmd/ctl", coverage = False}, args = ["b"], runtime = _on_host}
        fw.Workload {
            name = "image"
            image = "localhost:5050/image"
            build = forge.DockerBuild {coverage = {"./cmd/proxy" = False}}
            runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}}
        }
        fw.Workload {
            name = "image-plain"
            image = "localhost:5050/image2"
            build = forge.DockerBuild {}
            runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}}
        }
    ]
})

_b = {w.name: w.build for w in output.workloads}

assert_tests_rendered = output.tests.coverage == True
assert_packages_rendered = output.tests.coverage_packages == ["./internal/...", "./pkg/..."]
assert_unset_build_renders_no_coverage = "coverage" not in _b["plain"]
assert_build_override_rendered = _b["off"].coverage == False
assert_docker_keyed = _b["image"].coverage["./cmd/proxy"] == False
assert_docker_empty_renders_no_coverage = "coverage" not in _b["image-plain"]
