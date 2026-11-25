"""What llm-app runs, declared once. Each env's main.k binds it.

Written by `forge project new --in-place --kind app` from what it found in
this repo, and yours from here: forge never rewrites it. A `# check:` line is
something it assumed; read each one. `forge skill load adopt/deploy` explains every
field (Tier 1), and what to add next.
"""
import forge
import forge.workloads as fw
import forge.tiers

api = fw.Workload {
    name = "api"
    kind = "service"
    # Bare: hosting puts the build under <registry>/<org>/llm-app/api.
    image = "api"
    build = forge.DockerBuild {dockerfile = "Dockerfile", context = "."}
    ports = [fw.Port {name = "http", port = 3000, expose = True}]  # the Dockerfile's EXPOSE
    # forge.llm_env(): OPENAI_* / ANTHROPIC_* / RELIANT_LLM_* for the env's gateway.
    env = forge.llm_env() | {
        PORT = "3000"
        LLM_MODEL = "claude-haiku-4-5"
        # Missing on an unclaimed deploy: the HTTP handler fails closed.
        APP_API_TOKEN = forge.ManagedSecret {name = "APP_API_TOKEN", optional = True}
    }
    probes = tiers.Probes {readinessPath = "/healthz"}
}
