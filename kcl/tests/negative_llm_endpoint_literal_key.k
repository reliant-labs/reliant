"""
Negative test — a literal api_key for a remote endpoint is a key committed
to the repo. Only a local endpoint that ignores the key may take a literal.
"""

# expect: LLMEndpoint.api_key is a literal

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    llm = forge.LLM {runtime = forge.LLMEndpoint {base_url = "https://api.openai.com", api_key = "sk-live"}}
    workloads = [fw.Workload {
        name = "api"
        env = forge.llm_env()
        command = ["./bin/api"]
        runtime = forge.OnHost {}
    }]
})
