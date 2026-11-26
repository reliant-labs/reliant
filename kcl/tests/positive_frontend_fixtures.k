"""
Positive test — Frontend.fixtures is RUNTIME config: it renders into the
frontend's runtime config document on every runtime that serves one, as two
flat keys, and changes nothing about the build.

  * FIXTURES_SCENARIO carries the scenario name;
  * FIXTURES_READ_ONLY is always rendered, and is "true" unless the env says
    read_only = False (fail closed);
  * a dev server (OnHost) and your bucket (OnBucket) get them resolved, in the
    config.js forge writes itself;
  * OnHosted publishes them as literals in the StaticSite spec, and its
    resolved document stays empty (the control plane writes config.js);
  * a frontend with no fixtures renders neither key, and the env vars (the
    build) are identical with and without fixtures, so one release serves
    both envs.
"""

import forge

_web = forge.Frontend {
    name = "web", path = "web", type = "vite", port = 5173
    runtime = forge.OnHost {}
}

_all = forge.render(forge.Bundle {
    project = "shop", env = "demo"
    control_plane = forge.ControlPlane {endpoint = "https://cp.example"}
    frontends = [
        _web | {fixtures = forge.Fixtures {scenario = "shop"}}
        forge.Frontend {
            name = "sandbox", path = "sandbox", type = "nextjs", port = 3000
            runtime = forge.OnHost {}
            fixtures = forge.Fixtures {scenario = "empty-account", read_only = False}
        }
        forge.Frontend {
            name = "bucket", path = "bucket", type = "vite"
            runtime = forge.OnBucket {bucket = "gs://acme-demo"}
            runtime_config = {APP_NAME = "shop"}
            fixtures = forge.Fixtures {scenario = "shop"}
        }
        forge.Frontend {
            name = "hosted", image = "ghcr.io/acme/hosted", path = "hosted", type = "vite"
            runtime = forge.OnHosted {}
            fixtures = forge.Fixtures {scenario = "shop"}
        }
        forge.Frontend {
            name = "live", path = "live", type = "vite", port = 5174
            runtime = forge.OnHost {}
        }
    ]
})

# The same frontend without fixtures: what prod renders for it.
_prod = forge.render(forge.Bundle {
    project = "shop", env = "prod"
    frontends = [_web]
})

output = _all

_by = {f.name: f for f in _all.frontends}
_rc = lambda n: str -> any { _by[n].runtime_config }
_spec = lambda n: str -> any { _by[n].runtime_config_spec }

assert_scenario_rendered = _rc("web").FIXTURES_SCENARIO == "shop"
assert_read_only_by_default = _rc("web").FIXTURES_READ_ONLY == "true"
assert_read_only_off_is_explicit = _rc("sandbox").FIXTURES_READ_ONLY == "false" and _rc("sandbox").FIXTURES_SCENARIO == "empty-account"
assert_bucket_carries_both = _rc("bucket").FIXTURES_SCENARIO == "shop" and _rc("bucket").APP_NAME == "shop"
assert_hosted_spec_literal = _spec("hosted").FIXTURES_SCENARIO == {"value": "shop"} and _spec("hosted").FIXTURES_READ_ONLY == {"value": "true"}
assert_hosted_resolved_empty = len(_rc("hosted")) == 0
assert_live_has_none = "FIXTURES_SCENARIO" not in _rc("live") and "FIXTURES_READ_ONLY" not in _rc("live")
assert_build_unchanged = _by["web"].env_vars == _prod.frontends[0].env_vars and _by["web"].config == _prod.frontends[0].config
assert_prod_has_none = "FIXTURES_SCENARIO" not in _prod.frontends[0].runtime_config
