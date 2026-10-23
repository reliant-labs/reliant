"""
A small env that uses every runtime once (ADR 0002).

Render it through forge, which supplies `import forge` from its own binary:

    forge env render dev          # in a project
    # or, in this repo, the module test:
    go test ./internal/templates -run TestKCLModule_ExampleRendersOneOutput

In a project the workload declarations live in deploy/kcl/workloads.k and
every env imports them. They are inlined here to keep the example one file.
Top-level names are underscore-prefixed, so the render projects only
`output`.
"""

import forge
import forge.workloads as fw

# ── Declarations: WHAT runs. Complete on their own, shared by every env. ──

_build = forge.GoBuild {cmd = "./cmd/example", output_name = "example"}

# The image, registry INCLUDED. This is where a registry is declared — no env
# field carries one, so the reference you read here is the one the build pushes
# to and the cluster pulls. forge appends only the tag (and, after a push, the
# digest), so binding this same declaration to a second env changes the tag,
# never the registry. A workload that only ever runs OnHost needs no image.
_image = "ghcr.io/acme/example"

# A service built from this repo. `args` select the subcommand on every
# runtime: `go run ./cmd/example api` on the host, `example api` in a pod.
# forge writes its /readyz + /healthz probes into the spec.
_api = fw.Workload {
    name = "api"
    build = _build
    args = ["api"]
    ports = [fw.Port {name = "http", port = 8080, expose = True}]
    env = {
        LOG_LEVEL = "info"
        DATABASE_URL = forge.SecretRef {name = "example-secrets", key = "database_url"}
        WEB_URL = forge.WorkloadURL {workload = "admin-web"}
    }
}

# A one-shot that gates every other workload: schema migration.
_migrate = fw.Workload {
    name = "migrate"
    kind = "job"
    build = _build
    args = ["db", "migrate", "up"]
    before = [fw.BEFORE_ALL]
}

# A controller: cluster-scoped RBAC and the CRDs it owns.
_controller = fw.Workload {
    name = "workspace-controller"
    kind = "operator"
    image = _image
    build = _build
    args = ["workspace-controller"]
    crds = ["Workspace"]
    clusterRBAC = [fw.PolicyRule {apiGroups = ["example.dev"], resources = ["workspaces"], verbs = ["get", "list", "watch", "update", "patch"]}]
}

# A scheduled sweep.
_sweeper = fw.Workload {
    name = "billing-sweeper"
    kind = "cron"
    image = _image
    build = _build
    args = ["cron", "billing-sweep"]
    schedule = "@hourly"
}

# Third-party infrastructure the compose file defines. A compose service
# runs no image forge resolves, so it declares none.
_nats = fw.Workload {name = "nats"}

# A CLI shipped in the release, never run: two ldflag variants.
_cli = fw.Workload {
    name = "example-cli"
    kind = "tool"
    build = forge.GoBuild {cmd = "./cmd/example-cli"}
}

# ── The env: WHERE each one runs. ──

_k3d = forge.ClusterTarget {
    cluster = "k3d-example"
    namespace = "example-dev"
}

output = forge.render(forge.Bundle {
    project = "example"
    env = "dev"
    # WHERE each one runs, stated per workload: an env is a list of bindings.
    workloads = [
        _api | {runtime = forge.OnHost {runner = "air", air_config = ".air.toml", listen_ports = [8080]}}
        _migrate | {runtime = forge.OnHost {}}
        _controller | {runtime = forge.OnCluster {target = _k3d}}
        _sweeper | {runtime = forge.OnCluster {target = _k3d}}
        _nats | {runtime = forge.OnCompose {service = "nats"}}
        _cli | {runtime = forge.BuildOnly {build_variants = [
            forge.BuildVariant {name = "dev", ldflags = ["-X main.Env=dev"]}
            forge.BuildVariant {name = "prod", ldflags = ["-X main.Env=prod"]}
        ]}}
    ]
    # A host-run postgres the api dials; forge fetches and supervises it.
    infra = [forge.HostInfra {name = "postgres", port = 5433, database = "example"}]
    frontends = [forge.Frontend {name = "admin-web", type = "nextjs", path = "frontends/admin-web", runtime = forge.OnHost {}}]
    secret_provider = forge.FileSecrets {path = "secrets/dev.yaml"}
    # Gateway API ingress. In a project this lives in deploy/kcl/ingress.k.
    gateways = [forge.Gateway {
        name = "public"
        listeners = [forge.GatewayListener {name = "http", port = 18080, protocol = "HTTP"}]
    }]
    # A raw object forge applies with the env, on the primary cluster.
    manifests = [forge.Manifests {objects = [
        forge.crd("example.dev", "Workspace", "workspaces", "workspace")
    ]}]
    # External prerequisites forge does NOT create: a render-time checklist,
    # and a deploy preflight that BLOCKS on a missing required Secret.
    required_dns = [forge.DNSRecord {host = "*.workspaces.example.com", reason = "workspace-proxy wildcard"}]
})
