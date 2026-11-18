"""
Positive test — forge.OnExternal, the runtime binding whose deploy is the
author's own tooling (Helm, Fly.io, Cloud Run, a script).

  * the hooks render verbatim as argv, and an unset hook renders null (Go
    tells "no hook" from "an empty hook" by it);
  * no hooks at all is legal: the binding is record-only;
  * the spec image is the full pull reference, pinned to the deploy's
    digest when the deploy captured one (the same resolution OnCluster gets);
  * forge renders NO manifest for an external workload: nothing of it is
    applied by forge.
"""

# kcl-args: image_digests="{\"ghcr.io/acme/api\":\"sha256:1111111111111111111111111111111111111111111111111111111111111111\"}"

import forge
import forge.workloads as fw

_ext = forge.OnExternal {
    status = ["./scripts/status.sh"]
    plan = ["helm", "diff", "upgrade", "api", "./chart", "--set", "image=$FORGE_IMAGE", "--detailed-exitcode"]
    deploy = ["helm", "upgrade", "--install", "api", "./chart", "--set", "image=$FORGE_IMAGE"]
    health = ["curl", "-fsS", "https://api.acme.dev/readyz"]
    env = {KUBECONFIG = "/etc/kube/prod"}
    pass_env = True
}

output = forge.render(forge.Bundle {
    project = "acme"
    workloads = [
        fw.Workload {name = "api", image = "ghcr.io/acme/api", build = forge.GoBuild {cmd = "./cmd/api"}, runtime = _ext}
        fw.Workload {name = "worker", kind = "worker", image = "ghcr.io/acme/worker:v1", runtime = forge.OnExternal {}}
    ]
})

_wl = lambda n: str -> any {
    [w for w in output.workloads if w.name == n][0]
}

assert_type = _wl("api").runtime.type == "external"
assert_status_argv = _wl("api").runtime.status == ["./scripts/status.sh"]
assert_deploy_token_verbatim = _wl("api").runtime.deploy[-1] == "image=$FORGE_IMAGE"
assert_hook_env = _wl("api").runtime.env == {KUBECONFIG = "/etc/kube/prod"}
assert_pass_env = _wl("api").runtime.pass_env == True
assert_default_timeout = _wl("api").runtime.timeout == 900
assert_record_only_no_hooks = _wl("worker").runtime.status == None and _wl("worker").runtime.deploy == None and _wl("worker").runtime.plan == None and _wl("worker").runtime.health == None
assert_pinned_image = _wl("api").spec.image == "ghcr.io/acme/api@sha256:1111111111111111111111111111111111111111111111111111111111111111"
assert_declared_image = _wl("worker").spec.image == "ghcr.io/acme/worker:v1"
assert_no_manifest = len([m for m in output.manifests if m.kind == "Workload"]) == 0
