# Fixture for cmd/reliant/cmd TestDevSignerRenderRefusedOutsideDev: the
# api-server bound with the dev signer set and no tier at all, which the
# process reads as prod. Refused.
#
# Not an environment: there is no main.k here, and nothing imports this file.

import workloads

unset = (workloads.api_server | {env = {AUTH_DEV_JWT_SECRET = "dev-local-hmac-shared-secret-do-not-use-in-prod"}}).env
