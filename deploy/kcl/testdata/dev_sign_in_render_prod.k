# Fixture for cmd/reliant/cmd TestDevSignInFlagRenderRefusedOutsideDev: a prod
# bundle whose web frontend sets VITE_DEV_SIGN_IN. Refused.
#
# Not an environment: there is no main.k here, and nothing imports this file.

import forge
import dev_sign_in as dsi

prod = dsi.only_in_dev("prod", forge.Bundle {
    project = "reliant"
    env = "prod"
    control_plane = forge.ControlPlane {}
    frontends = [forge.Frontend {
        name = "web"
        type = "vite"
        path = "frontends/web"
        runtime = forge.OnHosted {}
        env_vars = [forge.EnvVar {name = dsi.WEB_FLAG_NAME, value = "true"}]
    }]
}).frontends[0].name
