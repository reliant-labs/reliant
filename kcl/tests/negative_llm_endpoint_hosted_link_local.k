"""
Negative test — from Reliant's network a link-local address is the
platform's (169.254.169.254 is the cloud metadata service), so a hosted
workload may not send an LLMEndpoint's requests and key there.
"""

# expect: names a host that is not a public DNS name
# expect: http://169.254.169.254

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    llm = forge.LLM {runtime = forge.LLMEndpoint {base_url = "http://169.254.169.254", api_key = forge.ManagedSecret {name = "LLM_KEY"}}}
    workloads = [fw.Workload {
        name = "api"
        image = _img
        env = forge.llm_env()
        runtime = forge.OnHosted {}
    }]
})
