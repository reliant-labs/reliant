"""
Negative test — a Next.js frontend whose build is a Node server, bound to a
static runtime.

forge.OnBucket publishes the files a static export writes to public_dir. A
frontend whose forge.yaml `output` is `standalone` writes none: its build is
a server under .next-prod/. Without the refusal the env renders, deploys and
builds green, and the failure arrives at publish time as a missing out/ —
or as a stale one, shipped as the new release.

`frontend_outputs` is the next.config fact forge binds on every render it
drives (internal/cli renderKCLRaw). The refusal names the frontend, the env
and both fixes.
"""

# kcl-args: env="prod" frontend_outputs="{\"web\":\"standalone\"}"
# expect: frontend 'web' cannot run on the bucket runtime
# expect: its next.config builds `output: standalone` for frontend 'web'
# expect: in env 'prod'
# expect: make the production build in frontends/web's next.config a static export
# expect: forge lint --static-export
# expect: forge.DockerBuild {dockerfile = "frontends/web/Dockerfile"}

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    frontends = [forge.Frontend {
        name = "web"
        path = "frontends/web"
        public_dir = "out"
        runtime = forge.OnBucket {bucket = "acme-web"}
    }]
})
