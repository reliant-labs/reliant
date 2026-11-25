# Fixture for internal/forgetest TestFixturesGuard: forge's `fixtures` field
# with a same-origin telemetry ingest (OTEL_ENDPOINT = "/_otel"). The ingest
# is a live service of the host, so it is refused like any other origin.
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
    fixtures = forge.Fixtures {scenario = "shop"}
    runtime_config = {
        API_URL = "https://api.preview.invalid"
        OTEL_ENDPOINT = "/_otel"
    }
}).name
