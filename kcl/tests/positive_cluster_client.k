"""
Positive tests for forge.ClusterClient — a workload's cross-cluster
client wiring stated ONCE.

Given `cluster` (the Cluster to reach), `in_cluster` (the Cluster the
workload runs in), and `mount_path`, a ClusterClient DERIVES the three
things a workload otherwise hand-correlates:

  * `.secret`      — the minted KubeconfigSecret (in_cluster -> cluster).
  * `.volume`      — the Volume mounting that Secret at mount_path.
  * `.config_json` — the [{id,kubeconfigPath,context}] JSON the workload
                     reads (CLUSTER_CONFIGS), with kubeconfigPath derived
                     from the SAME mount_path + key the volume/secret use.
  * `.config_entry`— the raw dict, for aggregating several clients.

Exercises:
  * secret name / target / in_cluster context are derived from the refs.
  * volume source == secret name, mounted at mount_path, read-only secret.
  * config_json's kubeconfigPath == mount_path + "/" + key, so it can't
    drift from the volume mount.
  * config_entry aggregates into a multi-cluster array.
  * The derived KubeconfigSecret can be emitted in the Bundle and is
    NEVER a rendered manifest.
"""

import forge
import forge.workloads as fw
import json

_cp = forge.Cluster { name = "control-plane", config = "deploy/k3d.yaml", ingress = True }
_daemon = forge.Cluster { name = "cp-daemon", owner = _cp, api_port = 6444 }

# A control-plane controller reaching INTO the cp-daemon cluster.
_daemon_link = forge.ClusterClient {
    id = "cp-daemon"
    cluster = _daemon
    in_cluster = _cp
    mount_path = "/etc/daemon-cluster"
    namespace = "control-plane-e2e"
}

# The inverse: a cp-daemon-hosted workload reaching BACK into control-plane.
_cp_link = forge.ClusterClient {
    id = "control-plane"
    cluster = _cp
    in_cluster = _daemon
    mount_path = "/etc/control-plane-cluster"
    namespace = "control-plane-e2e"
}

_target = forge.ClusterTarget {
    cluster = _cp.context
    namespace = "control-plane-e2e"
}

# A consuming service references link.volume + link.config_json; the
# derived secret is emitted in the Bundle's kubeconfig_secrets.
_consumer = fw.Workload {
    name = "workspace-controller"
    image = "localhost:5051/control-plane"
    env = {CLUSTER_CONFIGS = _daemon_link.config_json}
    volumes = [_daemon_link.volume]
    runtime = forge.OnCluster {target = _target}
}

_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _target
    clusters = [_cp, _daemon]
    kubeconfig_secrets = [_daemon_link.secret, _cp_link.secret]
    workloads = [_consumer]
}

output = forge.render(_bundle)
manifests = output.manifests

# ── Invariants ─────────────────────────────────────────────────────

# Secret: name = <id>-kubeconfig; target = the reached cluster's NAME;
# in_cluster = the running cluster's CONTEXT (k3d-<name>); context_name = id.
assert_secret_name = _daemon_link.secret.name == "cp-daemon-kubeconfig"
assert_secret_target = _daemon_link.secret.target_cluster == "cp-daemon"
assert_secret_in_cluster = _daemon_link.secret.in_cluster == "k3d-control-plane"
assert_secret_context = _daemon_link.secret.context_name == "cp-daemon"
assert_secret_ns = _daemon_link.secret.namespace == "control-plane-e2e"

# Volume: a read-only secret projection sourced from the SAME secret name,
# mounted at the declared mount_path.
assert_vol_name = _daemon_link.volume.name == "cp-daemon-kubeconfig"
assert_vol_source = _daemon_link.volume.source.secret.name == "cp-daemon-kubeconfig"
assert_vol_mount = _daemon_link.volume.mountPath == "/etc/daemon-cluster"
assert_vol_ro = _daemon_link.volume.readOnly == True

# config_json: the kubeconfigPath is derived from mount_path + "/" + key,
# so it ALWAYS agrees with the volume mount above — the drift this schema
# eliminates.
_cfg = json.decode(_daemon_link.config_json)
assert_cfg_len = len(_cfg) == 1
assert_cfg_id = _cfg[0].id == "cp-daemon"
assert_cfg_path = _cfg[0].kubeconfigPath == "/etc/daemon-cluster/kubeconfig"
assert_cfg_context = _cfg[0].context == "cp-daemon"
# The kubeconfigPath is mount_path + "/" + the secret key — provably
# consistent with the volume mount and the secret key.
assert_cfg_consistent = _cfg[0].kubeconfigPath == \
    _daemon_link.volume.mountPath + "/" + _daemon_link.secret.key

# config_entry aggregates: two clients into one CLUSTER_CONFIGS array.
_multi = json.encode([_daemon_link.config_entry, _cp_link.config_entry])
_multi_decoded = json.decode(_multi)
assert_multi_len = len(_multi_decoded) == 2
assert_multi_ids = _multi_decoded[0].id == "cp-daemon" and _multi_decoded[1].id == "control-plane"

# The consumer wires the derived volume + config_json with no hand paths.
_consumer_spec = [w for w in output.workloads if w.name == "workspace-controller"][0].spec
assert_consumer_env = [e for e in _consumer_spec.env if e.name == "CLUSTER_CONFIGS"][0].value == _daemon_link.config_json
# The derived volume reaches the workload spec in the generated Volume shape.
assert_consumer_volume = _consumer_spec.volumes[0].name == _daemon_link.volume.name and \
    _consumer_spec.volumes[0].mountPath == _daemon_link.volume.mountPath

# Both derived secrets ride the entity output, neither is a manifest.
assert_secrets_emitted = len(output.kubeconfig_secrets) == 2
assert_no_secret_manifest = len([m for m in manifests \
    if m.kind == "Secret" and m.metadata.name == "cp-daemon-kubeconfig"]) == 0
