"""
Negative test — a compose container's environment is the compose file's, and
the compose process env forge hands it holds literals, so an endpoint key
declared as a forge.ManagedSecret is refused for a compose referrer rather
than dropped. The message names the working path: the compose file reads the
key from the store by its store name.
"""

# expect: cannot reach a compose container
# expect: OPENAI_API_KEY: ${RELIANT_LLM_API_KEY}

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    llm = forge.LLM {runtime = forge.LLMEndpoint {
        base_url = "https://admin.reliantapi.com"
        api_key = forge.ManagedSecret {name = "RELIANT_LLM_API_KEY"}
    }}
    workloads = [fw.Workload {
        name = "indexer"
        kind = "worker"
        env = forge.llm_env()
        runtime = forge.OnCompose {}
    }]
})
