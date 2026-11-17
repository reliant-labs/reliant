"""
Negative test — a URL parser reads 0x7f.1 (hex, two parts) as 127.0.0.1
and 0xa9.0xfe.0xa9.0xfe (hex octets) as 169.254.169.254, the cloud
metadata service, so a hosted workload may not send an LLMEndpoint's
requests and key to either: a hosted endpoint is named by a DNS name, and
every IP literal is refused.
"""

# expect: names a host that is not a public DNS name
# expect: https://0x7f.1
# expect: https://0xa9.0xfe.0xa9.0xfe/v1

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    llm = forge.LLM {runtime = forge.LLMEndpoint {
        base_url = "https://0x7f.1"
        openai_base_url = "https://0xa9.0xfe.0xa9.0xfe/v1"
        api_key = forge.ManagedSecret {name = "LLM_KEY"}
    }}
    workloads = [fw.Workload {
        name = "api"
        image = _img
        env = forge.llm_env()
        runtime = forge.OnHosted {}
    }]
})
