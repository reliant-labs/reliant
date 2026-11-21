# Fixture for cmd/reliant/cmd TestDevSignInFlagRenderRefusedOutsideDev's
# accepted cases: the web frontend with VITE_DEV_SIGN_IN in the dev env (the
# intended dev config, as control-plane's dev declares it), and an ordinary
# prod bundle without it. The refused case is dev_sign_in_render_prod.k, apart
# because a refusal fails the whole file's evaluation.
#
# Not an environment: there is no main.k here, and nothing imports this file.

import forge
import dev_sign_in as dsi

_web = forge.Frontend {
    name = "web"
    type = "vite"
    path = "frontends/web"
}

dev = dsi.only_in_dev("dev", forge.Bundle {
    project = "reliant"
    env = "dev"
    frontends = [_web | {
        runtime = forge.OnHost {}
        env_vars = [forge.EnvVar {name = dsi.WEB_FLAG_NAME, value = "true"}]
    }]
}).frontends[0].env_vars[0].value

prod_without = dsi.only_in_dev("prod", forge.Bundle {
    project = "reliant"
    env = "prod"
    control_plane = forge.ControlPlane {}
    frontends = [_web | {
        runtime = forge.OnHosted {}
        env_vars = [forge.EnvVar {name = "VITE_API_URL", value = "https://api.example.com"}]
    }]
}).frontends[0].name
