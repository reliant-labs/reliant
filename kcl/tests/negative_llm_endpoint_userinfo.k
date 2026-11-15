"""
Negative test — a URL with user info (http://localhost@attacker.example)
reads as one host and dials another, so an LLMEndpoint URL may not carry it.
"""

# expect: LLMEndpoint.base_url 'http://localhost@attacker.example' must be a URL

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    llm = forge.LLM {runtime = forge.LLMEndpoint {base_url = "http://localhost@attacker.example", api_key = forge.ManagedSecret {name = "LLM_KEY"}}}
    workloads = [fw.Workload {
        name = "api"
        env = forge.llm_env()
        command = ["./bin/api"]
        runtime = forge.OnHost {}
    }]
})
