"""
Positive test — the IdP identity handoff as DATA, not a plugin call.

An `idp-provision` job converges the IdP and PUBLISHES the values it owns
(the generated OIDC client_id, the audience) to a ConfigMap it is granted
write on. A consumer reads them BY NAME through configMapRef. The two
declarations are written independently; this pins that they name the same
object and that the job's Role grants exactly the write the ConfigMap needs.

configMapRef is a Full-profile channel (cluster runtime only; the hosted
runtime refuses it — negative_runtime_mask_hosted_config_map_env.k).
Nothing here imports kcl_plugin.forge: there is no render-time IdP dial.
"""

import forge
import forge.workloads as fw

_CONFIGMAP = "demo-idp-identity"
_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [_w | {runtime = forge.OnCluster {target = _k3d}} if not _w.runtime else _w for _w in [
        fw.Workload {
            kind = "job"
            name = "idp-provision"
            image = "localhost:5050/demo"
            args = ["auth", "idp-provision", "--configmap-name", _CONFIGMAP]
            namespacedRBAC = [{apiGroups = [""], resources = ["configmaps"], verbs = ["get", "create", "update", "patch"]}]
        }
        fw.Workload {
            name = "admin-server"
            image = "localhost:5050/demo"
            args = ["admin"]
            ports = [fw.Port {name = "http", port = 8080, expose = True}]
            env = {
                OIDC_CLIENT_ID = forge.ConfigMapRef {name = _CONFIGMAP, key = "client_id"}
                JWT_AUDIENCE = forge.ConfigMapRef {name = _CONFIGMAP, key = "audience"}
            }
        }
    ]]
})

_rec = {m.metadata.name: m.spec for m in output.manifests if m.kind == "Workload"}
_env = {e.name: e for e in _rec["admin-server"].env}

assert_client_id_from_published_configmap = _env["OIDC_CLIENT_ID"].configMapRef == {name = _CONFIGMAP, key = "client_id"}
assert_audience_from_published_configmap = _env["JWT_AUDIENCE"].configMapRef == {name = _CONFIGMAP, key = "audience"}
assert_job_grant_on_its_spec = {apiGroups = [""], resources = ["configmaps"], verbs = ["get", "create", "update", "patch"]} in _rec["idp-provision"].namespacedRBAC
assert_job_names_the_same_configmap = _CONFIGMAP in _rec["idp-provision"].args
