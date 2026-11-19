"""
Negative test — an env that does NOT declare a lifecycle is a real
environment, and may not hold plaintext secrets, whatever it is called.

`demo` and `staging` are both refused here: neither names itself local, so
FileSecrets (which renders plaintext k8s Secrets) must not be its provider.
The fix the message names is to declare `lifecycle`, which forge then
verifies — or to use ExternalSecrets.
"""

# kcl-args: env=demo | env=staging
# expect: needs a local env
# expect: lifecycle

import forge
import forge.workloads as fw

_bundle = forge.Bundle {
    project = "fixture"
    workloads = [fw.Workload {
        name = "api"
        kind = "service"
        runtime = forge.OnHost {listen_ports = [8080]}
        command = ["./api"]
    }]
    secret_provider = forge.FileSecrets {path = "secrets/demo.yaml"}
}

output = forge.render(_bundle)
