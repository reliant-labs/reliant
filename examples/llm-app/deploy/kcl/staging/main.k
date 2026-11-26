"""staging: on Reliant hosting.

Each workload in workloads.k runs on the platform (forge.OnHosted), built
once and promoted between envs by digest. Credentials come from the
platform's write-only store: `forge secret set --env staging <NAME>` (the
value on stdin). Hosted workloads and databases need billing on your
organization; `forge env deploy` prints the link when it is missing.
"""
import forge
import forge.workloads as fw
import ..workloads as wl

_hosted = lambda w: fw.Workload -> fw.Workload {
    w | {runtime = forge.OnHosted {}}
}

_bundle = forge.Bundle {
    project = "llm-app"
    control_plane = forge.ControlPlane {}
    # Reliant's LLM gateway: the platform mints this env's key (forge skill load deploy/llm).
    llm = forge.LLM {runtime = forge.OnHosted {}}
    workloads = [
        _hosted(wl.api)
    ]
    secret_provider = forge.HostedSecrets {}
}
output = forge.render(_bundle)
