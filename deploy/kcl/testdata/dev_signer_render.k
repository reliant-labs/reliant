# Fixture for cmd/reliant/cmd TestDevSignerRenderRefusedOutsideDev's accepted
# cases: the api-server bound as an env binds it (`api_server | {env = ...}`),
# with the dev signer set in the dev tier, and without it in prod. The refused
# cases are dev_signer_render_prod.k and dev_signer_render_unset.k, apart
# because a refusal fails the whole file's evaluation.
#
# Not an environment: there is no main.k here, and nothing imports this file.

import workloads

dev = (workloads.api_server | {env = {RELIANT_ENV = "dev", AUTH_DEV_JWT_SECRET = "dev-local-hmac-shared-secret-do-not-use-in-prod"}}).env
prod_without = (workloads.api_server | {env = {RELIANT_ENV = "prod"}}).env
