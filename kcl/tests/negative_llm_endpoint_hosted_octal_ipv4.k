"""
Negative test — a URL parser reads 0177.0.0.1 (octal octets) as 127.0.0.1
and 2130706433 (one decimal number) as the same address, so a hosted
workload may not send an LLMEndpoint's requests and key to either: a hosted
endpoint is named by a DNS name, and every IP literal is refused.
"""

# expect: names a host that is not a public DNS name
# expect: https://0177.0.0.1
# expect: https://2130706433/v1

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    llm = forge.LLM {runtime = forge.LLMEndpoint {
        base_url = "https://0177.0.0.1"
        openai_base_url = "https://2130706433/v1"
        api_key = forge.ManagedSecret {name = "LLM_KEY"}
    }}
    workloads = [fw.Workload {
        name = "api"
        image = _img
        env = forge.llm_env()
        runtime = forge.OnHosted {}
    }]
})
