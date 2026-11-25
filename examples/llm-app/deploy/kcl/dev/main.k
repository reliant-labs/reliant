"""dev: the app as a host process.

`forge env up dev` runs each workload's own dev command on this machine
(install the app's dependencies first), with PORT set to a free port.
Logs land in .forge/logs/dev/.
"""
import forge
import forge.workloads as fw
import kcl_plugin.forge as plugin
import ..workloads as wl

_on_host = lambda w: fw.Workload, command: [str], port: int, cwd: str -> fw.Workload {
    w | {
        command = command
        env = w.env | ({PORT = str(port)} if port else {})
        runtime = forge.OnHost {listen_ports = [port] if port else [], working_dir = cwd}
    }
}

_api_port = plugin.resolve_port("llm-app-dev-api", 3000)

_bundle = forge.Bundle {
    project = "llm-app"
    lifecycle = "local"
    # The same gateway with your own `llm:invoke` token, stored with
    # `reliant forge secret set --env dev RELIANT_LLM_API_KEY` (billed to you).
    llm = forge.LLM {runtime = forge.LLMEndpoint {
        base_url = "https://admin.reliantapi.com"
        api_key = forge.ManagedSecret {name = "RELIANT_LLM_API_KEY"}
    }}
    workloads = [
        _on_host(wl.api, ["npm", "start"], _api_port, ".")  # check: dev runs package.json script "start"
    ]
    # Dev credentials: the gitignored secrets/dev.yaml (`NAME: value`).
    secret_provider = forge.FileSecrets {path = "secrets/dev.yaml"}
}
output = forge.render(_bundle)
