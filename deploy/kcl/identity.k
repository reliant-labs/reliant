"""
The users of reliant: who can sign in to the app, and how.

YOURS. Every env that runs the managed identity imports this ONE declaration;
only WHERE it runs differs, and each env's main.k says that:

  dev   `forge.IdentityOnHost {infra = "idp"}`: the dev IdP (Zitadel) that
        `forge env up dev` starts and converges, with mailpit beside it.
        forge fills reliant-api's jwt_issuer, jwt_audience, oidc_client_id,
        oidc_redirect_uri, idp_login_uri, idp_base and idp_broker_token from
        the result (forge.IdentityRef): nothing is copied by hand.
  prod  a new instance on the control plane's shared Zitadel, bound by
        control-plane's prod KCL (owner decision 2026-11-19; R4 of
        design-auth-swap). Not declared yet.

reliant-api serves the native sign-in (internal/app/login_broker.go,
`/auth/*`) and verifies the identity's tokens beside Supabase's until the
cutover (internal/auth TokenChain). reliant has no roles of its own: a user is
a user, and what they may do is decided per resource in the handlers.
`forge skill load deploy/managed-identity` has every field.
"""

import forge

IDENTITY = forge.ManagedIdentity {}

# The application and consumer of an env: reliant-api brokers the sign-in
# and is the one workload whose config the identity fills. Named, not
# derived: forge would derive one per service workload, and the worker and the
# gateway authenticate no user.
API_WORKLOAD = "reliant-api-server"

for_env = lambda env: str, runtime: forge.IdentityOnHost -> forge.ManagedIdentity {
    IDENTITY | {
        runtime = runtime
        applications = [forge.IdentityApplication {name = "${env}:api", workload = API_WORKLOAD}]
        consumers = [forge.IdentityConsumer {workload = API_WORKLOAD}]
    }
}
