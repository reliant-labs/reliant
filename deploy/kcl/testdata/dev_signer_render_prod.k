# Fixture for cmd/reliant/cmd TestDevSignerRenderRefusedOutsideDev: the
# api-server bound in a prod-tier env with the dev signer set. Refused.
#
# Not an environment: there is no main.k here, and nothing imports this file.

import workloads

prod = (workloads.api_server | {env = {RELIANT_ENV = "prod", AUTH_DEV_JWT_SECRET = "dev-local-hmac-shared-secret-do-not-use-in-prod"}}).env
