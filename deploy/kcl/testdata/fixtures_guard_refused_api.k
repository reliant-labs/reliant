# Fixture for internal/forgetest TestFixturesGuard: forge's `fixtures` field
# with a live API origin. Refused.
#
# Not an environment: there is no main.k here, and nothing imports this file.

import forge
import fixtures_guard as fg

refused = fg.inert_when_fixtures(forge.Bundle {
    project = "reliant"
    env = "preview"
    control_plane = forge.ControlPlane {}
    frontends = [forge.Frontend {
        name = "web"
        type = "vite"
        path = "frontends/web"
        image = "web"
        runtime = forge.OnHosted {}
        fixtures = forge.Fixtures {scenario = "shop"}
        runtime_config = {
            API_URL = "https://api.reliantlabs.io"
            SUPABASE_URL = "https://supabase.preview.invalid"
        }
    }]
}).frontends[0].name
