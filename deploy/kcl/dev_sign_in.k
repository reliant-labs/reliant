"""
The web app's dev sign-in flag is dev's alone (control-plane#2735).

VITE_DEV_SIGN_IN=true on a frontend is the BUILD flag that turns the web app's
dev sign-in on (frontends/web src/lib/devSignIn.ts): a browser signs in with a
locally minted token instead of a real Supabase login. A production build
compiles it out whatever the flag says; this refuses to RENDER it anywhere but
dev, so declaring it on a production frontend is a render error, not something
only the browser build catches. Its server half, AUTH_DEV_JWT_SECRET, is
refused outside the dev tier by workloads.k's APIServerWorkload.

Every env of this repo renders through `only_in_dev`, naming itself
(deploy/kcl/<env>/main.k: `output = forge.render(dsi.only_in_dev("<env>", ...))`);
cmd/reliant/cmd TestDevSignInFlagRenderRefusedOutsideDev proves that each does,
that a prod bundle declaring the flag is refused, and that an ordinary prod
bundle and the dev bundle render. control-plane, which declares its own
reliant-web frontend, may import this module too (`import reliant.dev_sign_in`).
"""

import forge

# The web app's build flag.
WEB_FLAG_NAME = "VITE_DEV_SIGN_IN"

# The one env the flag may appear in.
DEV_ENV = "dev"

# only_in_dev returns b unchanged, after refusing the render when env_name is
# not dev and any of b's frontends declares the dev sign-in flag.
only_in_dev = lambda env_name: str, b: forge.Bundle -> forge.Bundle {
    _flagged = ["frontend ${f.name}" for f in (b.frontends or []) for e in (f.env_vars or []) if e.name == WEB_FLAG_NAME]
    assert env_name == DEV_ENV or not _flagged, \
        "${WEB_FLAG_NAME} is a dev-only build flag: env ${env_name} sets it on ${_flagged}. " \
        "It signs a browser in with a locally minted token; only deploy/kcl/dev may set it " \
        "(reliant deploy/kcl/dev_sign_in.k, control-plane#2735)."
    b
}
