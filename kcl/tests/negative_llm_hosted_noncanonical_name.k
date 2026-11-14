"""
Negative test — on forge.OnHosted the platform fills the LLM variables by
their canonical names, so an LLMRef under any other name would stay blank
forever. It is refused at render instead.
"""

# expect: the platform fills the LLM's variables BY NAME
# expect: forge.llm_env()

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    llm = forge.LLM {runtime = forge.OnHosted {}}
    workloads = [fw.Workload {
        name = "api"
        image = _img
        env = {MY_LLM_KEY = forge.LLMRef {key = "api_key"}}
        runtime = forge.OnHosted {}
    }]
})
