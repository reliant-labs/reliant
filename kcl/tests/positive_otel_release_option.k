"""
Positive test — `-D release_version=` is how forge tells the render which
release it is building or deploying; it becomes `service.version`.

Bundle.release defaults to option("release_version"), so a main.k states
nothing. An all-digit version is a str (forge quotes the binding), and an
explicit `release = ...` in main.k beats the option.
"""

# kcl-args: release_version="20261010"

import forge
import forge.workloads as fw

_wl = fw.Workload {name = "api", build = forge.GoBuild {cmd = "./cmd/acme"}, args = ["server"], runtime = forge.OnHost {}}
_attrs = lambda o: any -> str {
    {e.name: e.value for e in o.workloads[0].spec.env}.OTEL_RESOURCE_ATTRIBUTES
}

_bound = forge.render(forge.Bundle {project = "acme", env = "prod", workloads = [_wl]})
_pinned = forge.render(forge.Bundle {project = "acme", env = "prod", release = "v-pinned", workloads = [_wl]})

assert_option_is_service_version = _attrs(_bound) == "deployment.environment.name=prod,service.namespace=acme,service.version=20261010"
assert_explicit_release_wins = _attrs(_pinned).endswith("service.version=v-pinned")
