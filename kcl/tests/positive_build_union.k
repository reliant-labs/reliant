"""
Positive test — the typed Build union (GoBuild | DockerBuild | ShellBuild |
RemoteBuild) on fw.Workload renders with a self-describing `type`
discriminator into output.workloads[].build. Consumed by build.go's dispatch
on build.type.

`build` is TYPED now (no `{str:any}` dict + build_of coercion): a dict can
no longer bind to the wrong union member, so the structural-first mis-bind
the old build_of existed to prevent is unrepresentable.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-dev", namespace = "dev"}

_go = fw.Workload {
    name = "trader"
    image = "localhost:5051/trader"
    build = forge.GoBuild {
        cmd = "./cmd/trader"
        goarch = "arm64"
        flags = ["-cover"]
        ldflags = ["-s", "-w", "-X main.version=1.2.3"]
        env = {"CGO_ENABLED" = "0"}
    }
    runtime = forge.OnCluster {target = _k3d}
}

_docker = fw.Workload {
    name = "gateway"
    image = "localhost:5051/gateway"
    build = forge.DockerBuild {
        dockerfile = "Dockerfile.gateway"
        platform = "linux/amd64"
        target = "runtime"
        build_args = {"VERSION" = "1.2.3"}
    }
    runtime = forge.OnCluster {target = _k3d}
}

_shell = fw.Workload {
    name = "sidecar"
    kind = "tool"
    build = forge.ShellBuild {cmd = "echo hi > out.txt"}
    runtime = forge.BuildOnly {}
}

# RemoteBuild — produced by a HOSTED build service from a repo+ref PIN,
# because a build service cannot read the caller's filesystem.
_remote = fw.Workload {
    name = "hosted"
    kind = "tool"
    build = forge.RemoteBuild {
        source = forge.GitSource {repo = "github.com/acme/api", ref = "v1.2.3", subdir = "svc"}
        dockerfile = "docker/Dockerfile"
        platform = "linux/arm64"
        target = "runtime"
        build_args = {"VERSION" = "1.2.3"}
        cpu_millicores = 4000
        memory_bytes = 17179869184
        cache_gib = 50
        timeout_seconds = 1800
    }
    runtime = forge.BuildOnly {}
}

_remote_defaults = fw.Workload {
    name = "hosted-defaults"
    kind = "tool"
    build = forge.RemoteBuild {source = forge.GitSource {repo = "github.com/acme/x", ref = "main"}}
    runtime = forge.BuildOnly {}
}

# No build: an image-only workload is "not built by forge".
_plain = fw.Workload {name = "plain", image = "ghcr.io/acme/plain:v1", runtime = forge.OnCluster {target = _k3d}}

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [_go, _docker, _shell, _remote, _remote_defaults, _plain]
})
_b = {w.name: w.build for w in output.workloads}

assert_go_type = _b["trader"].type == "go"
assert_go_cmd = _b["trader"].cmd == "./cmd/trader"
assert_go_goarch = _b["trader"].goarch == "arm64"
assert_go_flags = _b["trader"].flags == ["-cover"]
assert_go_ldflags = _b["trader"].ldflags == ["-s", "-w", "-X main.version=1.2.3"]
assert_go_env = _b["trader"].env["CGO_ENABLED"] == "0"

assert_docker_type = _b["gateway"].type == "docker"
assert_docker_dockerfile = _b["gateway"].dockerfile == "Dockerfile.gateway"
assert_docker_platform = _b["gateway"].platform == "linux/amd64"
assert_docker_target = _b["gateway"].target == "runtime"

assert_shell_type = _b["sidecar"].type == "shell"
assert_shell_cmd = _b["sidecar"].cmd == "echo hi > out.txt"

assert_remote_type = _b["hosted"].type == "remote"
# The SOURCE PIN is what makes a remote build possible at all.
assert_remote_repo = _b["hosted"].source.repo == "github.com/acme/api"
assert_remote_ref = _b["hosted"].source.ref == "v1.2.3"
assert_remote_subdir = _b["hosted"].source.subdir == "svc"
assert_remote_dockerfile = _b["hosted"].dockerfile == "docker/Dockerfile"
assert_remote_platform = _b["hosted"].platform == "linux/arm64"
# The size fields are the BILLING dimensions.
assert_remote_cpu = _b["hosted"].cpu_millicores == 4000
assert_remote_mem = _b["hosted"].memory_bytes == 17179869184
assert_remote_cache = _b["hosted"].cache_gib == 50
assert_remote_timeout = _b["hosted"].timeout_seconds == 1800
assert_remote_defaults = _b["hosted-defaults"].cpu_millicores == 2000 and _b["hosted-defaults"].timeout_seconds == 900

# No build declared => null, and NO synthesized ./cmd/<name> default: the
# lowering never guesses a package path (map §1.1 EffectiveBuild DEL).
assert_plain_build_null = _b["plain"] == None
