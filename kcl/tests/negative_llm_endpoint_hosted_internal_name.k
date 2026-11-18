"""
Negative test — an in-cluster name (*.svc) seen from a hosted workload is a
platform service, so an LLMEndpoint naming one is refused for a hosted
referrer.
"""

# expect: names a host that is not a public DNS name
# expect: http://litellm.platform.svc:4000

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    llm = forge.LLM {runtime = forge.LLMEndpoint {base_url = "http://litellm.platform.svc:4000", api_key = forge.ManagedSecret {name = "LLM_KEY"}}}
    workloads = [fw.Workload {
        name = "api"
        image = _img
        env = forge.llm_env()
        runtime = forge.OnHosted {}
    }]
})
