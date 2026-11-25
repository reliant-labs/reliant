"""
Positive test — FileSecrets is allowed in ANY env that declares itself local,
whatever the env is called.

FileSecrets renders plaintext secrets, so where it may be used is a question
about where those secrets can land, which `Bundle.lifecycle` answers (and the
lifecycle check verifies against every target cluster). It used to be gated
on the env's NAME — dev/e2e only — which refused a host-only `demo` env that
can put a secret nowhere but this machine, while a cluster env merely named
"dev" passed. Every binding below renders, including names that look like
real environments, because the declared lifecycle is what decides.
"""

# kcl-args: env=demo | env=staging | env=prod

import forge
import forge.workloads as fw

_bundle = forge.Bundle {
    project = "fixture"
    lifecycle = "local"
    workloads = [fw.Workload {
        name = "api"
        kind = "service"
        runtime = forge.OnHost {listen_ports = [8080]}
        command = ["./api"]
        env = {
            SESSION_SECRET = forge.SecretRef {name = "app", key = "SESSION_SECRET"}
        }
    }]
    secret_provider = forge.FileSecrets {
        path = "secrets/demo.yaml"
        generate = {"SESSION_SECRET" = forge.GeneratedSecret {bytes = 32, encoding = "hex"}}
    }
}

output = forge.render(_bundle)

assert_store_type = output.secret_provider.type == "file"
assert_store_path = output.secret_provider.path == "secrets/demo.yaml"
assert_lifecycle = output.lifecycle == "local"
