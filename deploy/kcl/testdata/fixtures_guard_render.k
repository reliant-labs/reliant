# Fixture for internal/forgetest TestFixturesGuard's accepted cases: a
# fixture-mode frontend whose origins are all empty or under .invalid, and a
# frontend not in fixture mode, whose live origins are none of the guard's
# business. The refused cases are fixtures_guard_refused_*.k, one per file,
# because a refusal fails the whole file's evaluation.
#
# Not an environment: there is no main.k here, and nothing imports this file.

import forge
import fixtures_guard as fg

_web = forge.Frontend {
    name = "web"
    type = "vite"
    path = "frontends/web"
    image = "web"
    runtime = forge.OnHosted {}
}

inert = fg.inert_when_fixtures(forge.Bundle {
    project = "reliant"
    env = "preview"
    control_plane = forge.ControlPlane {}
    frontends = [_web | {
        fixtures = forge.Fixtures {scenario = "shop"}
        runtime_config = {
            API_URL = "https://api.preview.invalid"
            SUPABASE_URL = "https://supabase.preview.invalid:8443/auth"
            APP_URL = ""
            OTEL_ENDPOINT = ""
        }
    }]
}).frontends[0].name

live_without_fixtures = fg.inert_when_fixtures(forge.Bundle {
    project = "reliant"
    env = "prod"
    control_plane = forge.ControlPlane {}
    frontends = [_web | {
        runtime_config = {
            API_URL = "https://api.reliantlabs.io"
            OTEL_ENDPOINT = "/_otel"
        }
    }]
}).frontends[0].name
