"""
Negative test — the literal-key exception is for an exact local host: a
remote name that merely starts with one (localhost.attacker.example) is a
remote endpoint, and its literal key is refused.
"""

# expect: LLMEndpoint.api_key is a literal

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    llm = forge.LLM {runtime = forge.LLMEndpoint {base_url = "http://localhost.attacker.example:11434", api_key = "sk-live"}}
    workloads = [fw.Workload {
        name = "api"
        env = forge.llm_env()
        command = ["./bin/api"]
        runtime = forge.OnHost {}
    }]
})
