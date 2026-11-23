"""
Positive test — Frontend.apis, named same-origin APIs (/_api/@<name>).

  * an OnHosted frontend renders `apis` as {name: workload}, each target a
    hosted workload of this env — a string name and a forge.WorkloadURL alike
    — and no resolved URLs: the control plane writes `/_api/@<name>`;
  * a DEV-SERVED frontend renders the same table plus each workload's dev URL
    where render knows it (forge env up resolves the rest after it assigns
    host ports);
  * a frontend that declares no apis renders no `apis` key at all, so every
    existing render is byte-identical.
"""

import forge
import forge.workloads as fw

_hosted = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    workloads = [
        fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnHosted {}}
        fw.Workload {name = "billing-api", image = "ghcr.io/acme/billing", runtime = forge.OnHosted {}}
        fw.Workload {name = "auth", image = "ghcr.io/acme/auth", runtime = forge.OnHosted {}}
    ]
    control_plane = forge.ControlPlane {}
    frontends = [
        forge.Frontend {
            name = "site", image = "ghcr.io/acme/site", path = "site", type = "vite", public_dir = "dist"
            runtime = forge.OnHosted {}
            runtime_config = {API_URL = forge.WorkloadURL {workload = "api"}}
            apis = {billing = "billing-api", auth = forge.WorkloadURL {workload = "auth"}}
        }
        forge.Frontend {
            name = "plain", image = "ghcr.io/acme/plain", path = "plain", type = "vite", public_dir = "dist"
            runtime = forge.OnHosted {}
        }
    ]
})

_dev = forge.render(forge.Bundle {
    project = "acme"
    env = "dev"
    workloads = [
        fw.Workload {name = "api", build = forge.GoBuild {cmd = "./cmd/acme"}, args = ["api"], runtime = forge.OnHost {listen_ports = [8085]}}
        fw.Workload {name = "billing-api", build = forge.GoBuild {cmd = "./cmd/billing"}, runtime = forge.OnHost {listen_ports = [8090]}}
        fw.Workload {name = "auth", build = forge.GoBuild {cmd = "./cmd/auth"}, runtime = forge.OnHost {}}
    ]
    frontends = [forge.Frontend {
        name = "web", path = "frontends/web", type = "nextjs", port = 3000
        runtime = forge.OnHost {}
        apis = {billing = "billing-api", auth = "auth"}
    }]
})

_site = [f for f in _hosted.frontends if f.name == "site"][0]
_plain = [f for f in _hosted.frontends if f.name == "plain"][0]
_web = _dev.frontends[0]

assert_hosted_apis = _site.apis == {billing = "billing-api", auth = "auth"}
assert_hosted_no_urls = _site.api_urls == {}
assert_default_api_untouched = _site.runtime_config_spec.API_URL.workloadURL.name == "api"
assert_undeclared_renders_no_key = "apis" not in _plain and "api_urls" not in _plain
assert_dev_apis = _web.apis == {billing = "billing-api", auth = "auth"}
assert_dev_known_url = _web.api_urls.billing == "http://localhost:8090"
assert_dev_unknown_url_left_to_env_up = "auth" not in _web.api_urls
