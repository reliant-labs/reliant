"""
Negative test — a hosted workload sends an LLMEndpoint's key in every
request, so a public endpoint over plaintext http:// (here the OpenAI URL,
beside an https:// root) would hand the credential to anyone on the path.
"""

# expect: is plaintext http://
# expect: http://llm.example.com/v1

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    llm = forge.LLM {runtime = forge.LLMEndpoint {
        base_url = "https://llm.example.com"
        openai_base_url = "http://llm.example.com/v1"
        api_key = forge.ManagedSecret {name = "LLM_KEY"}
    }}
    workloads = [fw.Workload {
        name = "api"
        image = _img
        env = forge.llm_env()
        runtime = forge.OnHosted {}
    }]
})
