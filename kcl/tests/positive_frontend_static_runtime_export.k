"""
Positive test — what the static-export refusal leaves alone.

  * a frontend whose next.config is a static export, on every static runtime;
  * a `standalone` frontend on the dev server (forge.OnHost), which runs it
    and publishes nothing;
  * a frontend forge.yaml does not declare at all (a cross-repo `source`
    frontend, a Vite app declared only here): its build shape is unknown,
    and nothing is refused on a guess.

The same fixture with no `frontend_outputs` binding — a plain `kcl run` —
is the positive_frontend_runtime_* corpus, which refuses nothing.
"""

# kcl-args: env="prod" frontend_outputs="{\"web\":\"static\",\"docs\":\"static\",\"site\":\"static\",\"dev-web\":\"standalone\"}" | env="prod"
# The SAME declaration, with forge.yaml saying web's build is a server:
# reject-args: env="prod" frontend_outputs="{\"web\":\"standalone\",\"docs\":\"static\",\"site\":\"static\",\"dev-web\":\"standalone\"}"
# reject-expect: frontend 'web' cannot run on the hosted runtime

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    control_plane = forge.ControlPlane {}
    frontends = [
        forge.Frontend {name = "web", image = "ghcr.io/acme/web", path = "frontends/web", runtime = forge.OnHosted {}}
        forge.Frontend {name = "docs", path = "frontends/docs", runtime = forge.OnBucket {bucket = "acme-docs"}}
        forge.Frontend {name = "site", path = "frontends/site", runtime = forge.OnFirebase {project = "acme", site = "acme-site"}}
        forge.Frontend {name = "dev-web", path = "frontends/dev-web", runtime = forge.OnHost {}}
        forge.Frontend {name = "other", type = "vite", path = "other", runtime = forge.OnBucket {bucket = "acme-other"}}
    ]
})

_types = {f.name: f.runtime.type for f in output.frontends}
assert_static_on_hosted = _types["web"] == "hosted"
assert_static_on_bucket = _types["docs"] == "bucket"
assert_static_on_firebase = _types["site"] == "firebase"
assert_server_on_host = _types["dev-web"] == "host"
assert_undeclared_unjudged = _types["other"] == "bucket"
