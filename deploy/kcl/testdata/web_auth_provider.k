# Production-shaped cloud runtime config, through the typed projection used
# by deploying projects. This repo does not own the production environment.
import frontend_config_gen

_cloud = frontend_config_gen.ReliantWebConfig {
    auth_mode = "cloud"
    api_url = "https://api.example.test"
    control_plane_api_url = "https://admin.example.test"
    supabase_url = "https://dash.example.test"
    supabase_anon_key = "test-anon-key"
}

# Existing deployments omit auth_provider and must keep Supabase sign-in.
provider_default = frontend_config_gen.reliantWebConfigRuntime(_cloud)

# Opt-in must survive the typed projection into /config.js.
provider_forge = frontend_config_gen.reliantWebConfigRuntime(frontend_config_gen.ReliantWebConfig {
    **_cloud
    auth_provider = "forge"
})
