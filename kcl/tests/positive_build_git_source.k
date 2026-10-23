"""Cross-repo pins survive both local build mechanisms (#107)."""
import forge
import forge.workloads as fw

_src = forge.GitSource {repo = "github.com/acme/lib", ref = "v1.2.3", subdir = "src"}
_shell = fw.Workload {
    name = "shell"
    kind = "tool"
    build = forge.ShellBuild {cmd = "make build", source = _src}
    runtime = forge.BuildOnly {}
}
_docker = fw.Workload {
    name = "docker"
    kind = "tool"
    build = forge.DockerBuild {
        build_contexts = {lib = _src, base = "docker-image://alpine:3.22", local = "./local"}
    }
    runtime = forge.BuildOnly {}
}
output = forge.render(forge.Bundle {project = "demo", workloads = [_shell, _docker]})
assert_shell_source = output.workloads[0].build.source.repo == _src.repo and output.workloads[0].build.source.ref == _src.ref and output.workloads[0].build.source.subdir == _src.subdir
assert_shell_cwd = output.workloads[0].build.cwd == None
assert_docker_source = output.workloads[1].build.build_contexts.lib.repo == _src.repo and output.workloads[1].build.build_contexts.lib.ref == _src.ref
assert_docker_base = output.workloads[1].build.build_contexts.base == "docker-image://alpine:3.22"
assert_docker_local = output.workloads[1].build.build_contexts.local == "./local"
