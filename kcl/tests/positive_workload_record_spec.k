"""
Positive test — the §9.1 invariant P2c pins: for every cluster workload,
`output.workloads[].spec` equals the matching `output.manifests[kind=Workload].spec`
BYTE FOR BYTE (after JSON encoding), and a non-cluster workload has no record.

Two readers consume the same spec: Go's build/launch/publish paths read
output.workloads, and ExtractManifests expands the record. If the two ever
diverge (say the record resolves an image the entity does not, or drops an
env channel), `forge env render` and `forge env deploy` would describe
different workloads — the class of drift ADR 0002 exists to remove.

Covers every WorkloadSpec field family so a field added to the generated
workload_json but forgotten in one of the two projections fails here.
"""

import json
import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

_all = [
    fw.Workload {
        name = "api"
        build = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}
        image = "localhost:5050/demo"
        command = ["/app/demo"]
        args = ["api"]
        replicas = 2
        resources = fw.Resources {cpuRequestMillicores = 500, cpuLimitMillicores = 1000, memoryRequestBytes = 268435456, memoryLimitBytes = 536870912}
        ports = [fw.Port {name = "http", port = 8080, expose = True, appProtocol = "kubernetes.io/h2c"}]
        env = {
            A = "1"
            B = forge.SecretRef {name = "s", key = "k"}
            C = forge.ConfigMapRef {name = "c", key = "k"}
            D = forge.FieldRef {path = "metadata.name"}
        }
        namespacedRBAC = [{apiGroups = [""], resources = ["configmaps"], verbs = ["get"]}]
        serviceAccountAnnotations = {"iam.gke.io/gcp-service-account" = "api@p.iam.gserviceaccount.com"}
        sidecars = [fw.Container {name = "proxy", image = "gcr.io/cloud-sql-connectors/cloud-sql-proxy:2.14.1", args = ["--port=5432"]}]
        volumes = [fw.Volume {name = "cfg", mountPath = "/etc/cfg", readOnly = True, source = fw.VolumeSource {configMap = fw.ConfigMapVolumeSource {name = "cfg"}}}]
        nodeSelector = {"pool" = "general"}
        tolerations = [fw.Toleration {key = "dedicated", operator = "Equal", value = "general", effect = "NoSchedule"}]
        podAnnotations = {"prometheus.io/scrape" = "true"}
        priorityClassName = "demo-platform"
        enableServiceLinks = True
        strategy = "Recreate"
    }
    fw.Workload {name = "store", image = "localhost:5050/demo", args = ["store"], ports = [fw.Port {name = "http", port = 8080}], storageGiB = 10, serves = ["blobs", "index"]}
    fw.Workload {name = "nightly", kind = "cron", image = "localhost:5050/demo", args = ["sweep"], schedule = "@daily"}
    fw.Workload {name = "migrate", kind = "job", image = "localhost:5050/demo", args = ["db", "migrate", "up"], before = [fw.BEFORE_ALL]}
    fw.Workload {name = "report", kind = "job", image = "localhost:5050/demo", args = ["report"], deployPhase = "post-rollout"}
    fw.Workload {
        name = "mgr"
        kind = "operator"
        image = "localhost:5050/demo"
        args = ["mgr"]
        group = "example.com"
        version = "v1"
        crds = ["Widget"]
        leaderElection = False
        clusterRBAC = [{apiGroups = ["example.com"], resources = ["widgets"], verbs = ["list"]}]
    }
]

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    workloads = [_w | {runtime = forge.OnCluster {target = _k3d}} if not _w.runtime else _w for _w in _all + [fw.Workload {name = "local", build = forge.GoBuild {cmd = "./cmd/demo"}, args = ["local"], runtime = forge.OnHost {}}]]
})

_entity = {w.name: w.spec for w in output.workloads}
_record = {m.metadata.name: m.spec for m in output.manifests if m.kind == "Workload"}

assert_record_per_cluster_workload = sorted([k for k in _record]) == sorted([w.name for w in _all])
assert_no_record_for_host = "local" not in _record
# THE invariant: byte-equal JSON, for every record.
assert_spec_byte_equal = all n in _record {
    json.encode(_record[n], sort_keys=True) == json.encode(_entity[n], sort_keys=True)
}
# And not vacuously: the covered fields actually made it into the spec.
_api = _record["api"]
assert_fields_present = all k in ["kind", "image", "command", "args", "replicas", "resources", "ports", "env", "namespacedRBAC", "serviceAccountAnnotations", "sidecars", "volumes", "nodeSelector", "tolerations", "podAnnotations", "priorityClassName", "enableServiceLinks", "strategy", "probes"] {
    k in _api
}
# The opt-in reaches the wire spec; a workload that never sets it carries nothing.
assert_service_links_opt_in = _api.enableServiceLinks == True and "enableServiceLinks" not in _record["store"]
assert_storage = _record["store"].storageGiB == 10
assert_cron = _record["nightly"].schedule == "@daily" and _record["nightly"].kind == "cron"
assert_before = _record["migrate"].before == ["*"]
assert_deploy_phase = _record["report"].deployPhase == "post-rollout"
assert_operator = _record["mgr"].crds == ["Widget"] and _record["mgr"].group == "example.com" and _record["mgr"].leaderElection == False
# Authoring-only fields never reach the wire spec.
assert_no_authoring_fields = all n in _record {
    all k in ["name", "build", "runtime", "manifests", "config_secrets", "serves"] { k not in _record[n] }
}
