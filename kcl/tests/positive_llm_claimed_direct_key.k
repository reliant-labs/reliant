"""
Claim-first recovery: a hosted backend reads a direct provider key from the
managed store. This proves render/secret wiring, NOT public API connectivity
or inference; the user must claim, fund the provider key and verify a request.
Keep this example aligned with docs/forge/llm-provider.mdx in reliant.
"""
import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "llm-recovery"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
        env = {
            OPENAI_BASE_URL = "https://api.openai.com/v1"
            OPENAI_API_KEY = forge.ManagedSecret {name = "OPENAI_API_KEY"}
        }
        runtime = forge.OnHosted {}
    }]
})
_env = {e.name: e for e in output.workloads[0].spec.env}
assert_direct_url = _env.OPENAI_BASE_URL.value == "https://api.openai.com/v1"
assert_direct_secret = _env.OPENAI_API_KEY.managedSecret.name == "OPENAI_API_KEY"
assert_no_literal_key = not _env.OPENAI_API_KEY.value
