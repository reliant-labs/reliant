"""
Positive test — a `config_secrets` name the env DELIVERS renders: a managed
secret in env (any language), and a family pattern, which projects zero or more
names and is not judged. The negative twin is
negative_config_secrets_undelivered.k.
"""

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    workloads = [
        fw.Workload {
            name = "api"
            command = ["node", "dist/main.js"]
            config_secrets = ["JWT_SECRET", "APP_OAUTH_<KEY>_CLIENT_SECRET"]
            env = {JWT_SECRET = forge.ManagedSecret {name = "JWT_SECRET"}}
            runtime = forge.OnHost {listen_ports = []}
        }
    ]
})

assert_rendered = len(output.workloads) == 1
