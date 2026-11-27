"""What fastapi-postgres runs, declared once. Each env's main.k binds it.

Written by `forge project new --in-place --kind app` from what it found in
this repo, and yours from here: forge never rewrites it. A `# check:` line is
something it assumed; read each one. `forge skill load adopt/deploy` explains every
field (Tier 1), and what to add next.
"""
import forge
import forge.workloads as fw
import forge.tiers

api = fw.Workload {
    name = "api"
    kind = "service"
    # Bare: hosting puts the build under <registry>/<org>/fastapi-postgres/api.
    image = "api"
    build = forge.DockerBuild {dockerfile = "Dockerfile", context = "."}
    ports = [fw.Port {name = "http", port = 8000, expose = True}]  # the container port compose service "api" publishes
    env = {PORT = "8000"}
    # Ready once /healthz answers, which also proves the database is reachable.
    probes = tiers.Probes {readinessPath = "/healthz"}
}

# Schema migrations, from the same image, before every rollout. The image
# must carry the migration tool. Keep migrations backward-compatible: the
# running release serves while they apply.
migrate = fw.Workload {
    name = "migrate"
    kind = "job"
    image = api.image
    build = api.build
    command = ["python", "-m", "app.migrate"]  # check: compose service "migrate"'s command
    env = api.env
    before = [fw.BEFORE_ALL]
}
