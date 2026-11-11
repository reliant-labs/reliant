# Fixture for cmd/reliant/cmd TestAuthCookieDomainReachesTheWebRuntimeConfig:
# the reliant-web runtime config document (/config.js) as a deployment
# projects it, through the same frontend_config_gen.reliantWebConfigRuntime
# every env's reliant-web uses (control-plane's prod/reliant-web included),
# once with the schema default and once with the shared session domain set.
#
# Not an environment: there is no main.k here, and nothing imports this file.

import frontend_config_gen

_cloud = lambda domain: str -> frontend_config_gen.ReliantWebConfig {
    frontend_config_gen.ReliantWebConfig {
        auth_mode = "cloud"
        api_url = "https://api.example.test"
        control_plane_api_url = "https://admin.example.test"
        supabase_url = "https://dash.example.test"
        supabase_anon_key = "test-anon-key"
        auth_cookie_domain = domain
    }
}

# The schema default: no shared session, every host keeps its own.
cookie_default = frontend_config_gen.reliantWebConfigRuntime(frontend_config_gen.ReliantWebConfig {})

# Prod's shape with the shared session turned on.
cookie_shared = frontend_config_gen.reliantWebConfigRuntime(_cloud(".reliantlabs.io"))
