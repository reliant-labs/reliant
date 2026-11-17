"""
Negative test — a hosted workload cannot read a Kubernetes Secret, so an
endpoint whose key is a forge.SecretRef is refused for a hosted referrer.
"""

# expect: LLMEndpoint.api_key is a forge.SecretRef

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    llm = forge.LLM {runtime = forge.LLMEndpoint {base_url = "https://llm.example.com", api_key = forge.SecretRef {name = "llm", key = "key"}}}
    workloads = [fw.Workload {
        name = "api"
        image = _img
        env = forge.llm_env()
        runtime = forge.OnHosted {}
    }]
})
