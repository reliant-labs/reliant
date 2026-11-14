"""A deploy repo builds another repo's image: DockerBuild.context and
DockerBuild.dockerfile accept a forge.GitSource beside a path, and the pin
survives the render as its stable dict form. A path still renders as before."""
import forge
import forge.workloads as fw

_app = forge.GitSource {repo = "github.com/acme/billing", ref = "v2.3.1"}
_df = forge.GitSource {repo = "github.com/acme/images", ref = "v1.0.0", subdir = "python/Dockerfile"}
_pinned = fw.Workload {
    name = "billing"
    kind = "service"
    build = forge.DockerBuild {context = _app}
    runtime = forge.BuildOnly {}
}
_both = fw.Workload {
    name = "worker"
    kind = "service"
    build = forge.DockerBuild {context = _app, dockerfile = _df}
    runtime = forge.BuildOnly {}
}
_path = fw.Workload {
    name = "local"
    kind = "service"
    build = forge.DockerBuild {context = "frontends/web", dockerfile = "frontends/web/Dockerfile"}
    runtime = forge.BuildOnly {}
}
output = forge.render(forge.Bundle {project = "deploy", workloads = [_pinned, _both, _path]})
assert_context_pin = output.workloads[0].build.context.repo == _app.repo and output.workloads[0].build.context.ref == _app.ref and output.workloads[0].build.context.subdir == None
assert_default_dockerfile = output.workloads[0].build.dockerfile == "Dockerfile"
assert_dockerfile_pin = output.workloads[1].build.dockerfile.repo == _df.repo and output.workloads[1].build.dockerfile.subdir == "python/Dockerfile"
assert_path_context = output.workloads[2].build.context == "frontends/web" and output.workloads[2].build.dockerfile == "frontends/web/Dockerfile"
