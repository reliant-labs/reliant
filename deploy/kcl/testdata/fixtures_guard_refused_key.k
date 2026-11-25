# Fixture for internal/forgetest TestFixturesGuard: fixture mode turned on by
# its runtime-config key set by hand, with a live Supabase origin, and an
# origin that only looks invalid (invalid.example.com). Refused.
#
# Not an environment: there is no main.k here, and nothing imports this file.

import forge
import fixtures_guard as fg

refused = fg.inert_frontend(forge.Frontend {
    name = "web"
    type = "vite"
    path = "frontends/web"
    image = "web"
    runtime = forge.OnHosted {}
    runtime_config = {
        FIXTURES_SCENARIO = "shop"
        API_URL = "https://api.invalid.example.com"
        SUPABASE_URL = "https://abcdefgh.supabase.co"
    }
}).name
