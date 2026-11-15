"""
Negative test — a hosted workload's LLMEndpoint is named by a DNS name. An IP
literal is refused in every form, a public canonical dotted quad
(203.0.113.7) and a malformed quad-like host (127.0.0.1.999) alike: a render
has no IP parser to classify one safely, and a URL parser may read a
malformed one as an internal address.
"""

# expect: names a host that is not a public DNS name
# expect: https://203.0.113.7:8443
# expect: https://127.0.0.1.999/v1

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    llm = forge.LLM {runtime = forge.LLMEndpoint {
        base_url = "https://203.0.113.7:8443"
        openai_base_url = "https://127.0.0.1.999/v1"
        api_key = forge.ManagedSecret {name = "LLM_KEY"}
    }}
    workloads = [fw.Workload {
        name = "api"
        image = _img
        env = forge.llm_env()
        runtime = forge.OnHosted {}
    }]
})
