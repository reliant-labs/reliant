"""
Positive test — the dev loop: the identity runs on the env's host-run Zitadel,
a seed user exists (a local env), and the API reads its config from the env's
secret store by env var name.
"""
# kcl-args: env="dev"

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    env = "dev"
    lifecycle = "local"
    infra = [
        forge.HostInfra {name = "pg", engine = "postgres", port = 5432}
        forge.HostInfra {name = "idp", engine = "zitadel", port = 8098, idp_database_port = 5432}
    ]
    identity = forge.ManagedIdentity {
        runtime = forge.IdentityOnHost {infra = "idp"}
        roles = [forge.IdentityRole {key = "admin"}]
        seed_users = [forge.IdentitySeedUser {email = "dev@acme.test", roles = ["admin"], password_secret = forge.ManagedSecret {name = "DEV_PASSWORD"}}]
    }
    workloads = [
        fw.Workload {name = "api", kind = "service", runtime = forge.OnHost {runner = "binary"}, build = forge.GoBuild {cmd = "./cmd/acme", output_name = "acme"}, args = ["server"]}
    ]
})

_env = {e.name: e for e in {w.name: w for w in output.workloads}["api"].spec.env}
_id = output.identity

assert_runtime_is_host = _id.runtime == "host"
assert_seed_user_in_the_shape = _id.seed_users[0].email == "dev@acme.test" and _id.seed_users[0].password_secret == "DEV_PASSWORD"
assert_application_derived = _id.applications[0].name == "dev:api"
assert_issuer_from_the_store = _env["JWT_ISSUER"].secretRef.key == "ISSUER"
assert_client_id_from_the_store = _env["OIDC_CLIENT_ID"].secretRef.key == "DEV_API_CLIENT_ID"
assert_broker_token_is_an_env_value_on_host = _env["IDP_BROKER_TOKEN"].secretRef.key == "DEV_API_BROKER_TOKEN"
assert_nothing_is_delivered = len(output.control_plane_secrets) == 0 and len(_id.deliveries) == 0
