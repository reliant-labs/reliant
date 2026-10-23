"""
Positive test — forge.ClusterClient external/reference mode.

The default ClusterClient MINTS its kubeconfig Secret via
`k3d kubeconfig get` (covered by positive_cluster_client.k). dev/prod
kubeconfigs are instead provided OUT-OF-BAND (a bootstrap script for dev;
GKE for prod, reachability="endpoint"). `external = True` switches the
client to REFERENCE a pre-existing Secret rather than mint one:

  * `.secret` is None/Undefined — forge mints nothing, so the client
    contributes NO entry to the Bundle's kubeconfig_secrets and the Go
    mint step has nothing to do.
  * `.volume` sources the pre-existing Secret named by `volume_name`,
    with a chosen (decoupled) volume name — this is what lets a consumer
    that already mounts a hand-named Secret migrate render-equivalently.
  * `.config_json` / `.config_entry` derive from mount_path + key exactly
    as in minting mode.
"""

import forge
import forge.workloads as fw
import json

_prod = forge.Cluster { name = "workload-prod" }
_cp = forge.Cluster { name = "control-plane-prod" }

# A prod controller reaching into a workload cluster whose kubeconfig is
# provided out-of-band (GKE). No mint; endpoint reachability; the Volume
# sources an EXISTING, hand-named Secret.
_link = forge.ClusterClient {
    id = "workload"
    cluster = _prod
    in_cluster = _cp
    mount_path = "/etc/workload-cluster"
    namespace = "control-plane-prod"
    external = True
    reachability = "endpoint"
    volume_name = "workload-kubeconfig-oob"  # pre-existing Secret name
}

_target = forge.ClusterTarget {
    cluster = _cp.context
    namespace = "control-plane-prod"
}

_consumer = fw.Workload {
    name = "workspace-controller"
    image = "ghcr.io/reliant/control-plane/control-plane"
    env = {CLUSTER_CONFIGS = _link.config_json}
    volumes = [_link.volume]
    runtime = forge.OnCluster {target = _target}
}

# The external client emits NO secret into kubeconfig_secrets (it's
# Undefined). The Bundle's list is left empty.
_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _target
    clusters = [_cp, _prod]
    workloads = [_consumer]
}

output = forge.render(_bundle)
manifests = output.manifests

# ── Invariants ─────────────────────────────────────────────────────

# external mode mints NOTHING — .secret is Undefined.
assert_no_secret = _link.secret == Undefined

# The Volume sources the pre-existing Secret by the chosen volume_name —
# NOT the <id>-kubeconfig derivation.
assert_vol_name = _link.volume.name == "workload-kubeconfig-oob"
assert_vol_source = _link.volume.source.secret.name == "workload-kubeconfig-oob"
assert_vol_mount = _link.volume.mountPath == "/etc/workload-cluster"
assert_vol_ro = _link.volume.readOnly == True

# config_json/config_entry derive from mount_path + key exactly as minting
# mode — the kubeconfigPath agrees with the volume mount.
_cfg = json.decode(_link.config_json)
assert_cfg_len = len(_cfg) == 1
assert_cfg_id = _cfg[0].id == "workload"
assert_cfg_path = _cfg[0].kubeconfigPath == "/etc/workload-cluster/kubeconfig"
assert_cfg_context = _cfg[0].context == "workload"

# No KubeconfigSecret rides the entity output (the Bundle declared none),
# and certainly no minted Secret manifest is rendered.
assert_no_secrets_emitted = len(output.kubeconfig_secrets) == 0
assert_no_secret_manifest = len([m for m in manifests \
    if m.kind == "Secret" and m.metadata.name == "workload-kubeconfig-oob"]) == 0

# The consumer still wires the derived volume + config_json.
_consumer_spec = [w for w in output.workloads if w.name == "workspace-controller"][0].spec
assert_consumer_env = [e for e in _consumer_spec.env if e.name == "CLUSTER_CONFIGS"][0].value == _link.config_json
# The derived volume reaches the workload spec in the generated Volume shape.
assert_consumer_volume = _consumer_spec.volumes[0].name == _link.volume.name and \
    _consumer_spec.volumes[0].mountPath == _link.volume.mountPath

# ── A minting-mode client with an OVERRIDDEN volume_name still mints ──
# volume_name decouples the name but does NOT disable minting; only
# external = True does. The minted Secret AND the Volume share the pinned
# name (they can never drift), so the workload mounts exactly what's minted.
_pinned = forge.ClusterClient {
    id = "daemon"
    cluster = _prod
    in_cluster = _cp
    mount_path = "/etc/daemon-cluster"
    volume_name = "daemon-kubeconfig-pinned"
}
assert_pinned_mints = _pinned.secret != Undefined
assert_pinned_secret_name = _pinned.secret.name == "daemon-kubeconfig-pinned"
assert_pinned_vol_name = _pinned.volume.name == "daemon-kubeconfig-pinned"
assert_pinned_vol_source = _pinned.volume.source.secret.name == "daemon-kubeconfig-pinned"

# ── secret_name decouples the volume NAME from the sourced Secret NAME ──
# dev/prod mount an out-of-band kubeconfig under a volume named
# `daemon-cluster-kubeconfig` but source it from a Secret named
# `cp-daemon-kubeconfig`. The two differ, so `volume_name` alone (which sets
# BOTH name and source) can't model it — `secret_name` pins the source.
_split = forge.ClusterClient {
    id = "cp-daemon"
    cluster = _prod
    in_cluster = _cp
    mount_path = "/etc/daemon-cluster"
    external = True
    reachability = "endpoint"
    volume_name = "daemon-cluster-kubeconfig"  # pod volume name
    secret_name = "cp-daemon-kubeconfig"       # sourced (out-of-band) Secret name
}
# The volume keeps its own NAME but SOURCES the differently-named Secret.
assert_split_vol_name = _split.volume.name == "daemon-cluster-kubeconfig"
assert_split_vol_source = _split.volume.source.secret.name == "cp-daemon-kubeconfig"
assert_split_no_secret = _split.secret == Undefined
# config_json still derives from mount_path + key — unaffected by the name split.
_split_cfg = json.decode(_split.config_json)
assert_split_cfg_id = _split_cfg[0].id == "cp-daemon"
assert_split_cfg_path = _split_cfg[0].kubeconfigPath == "/etc/daemon-cluster/kubeconfig"
assert_split_cfg_context = _split_cfg[0].context == "cp-daemon"

# ── secret_name in MINTING mode keeps mint+source self-consistent ──
# When secret_name is set in minting mode, BOTH the minted Secret and the
# volume's source follow it (so the volume mounts exactly what's minted),
# while the volume NAME follows volume_name.
_split_mint = forge.ClusterClient {
    id = "daemon"
    cluster = _prod
    in_cluster = _cp
    mount_path = "/etc/daemon-cluster"
    volume_name = "daemon-vol"
    secret_name = "daemon-secret"
}
assert_split_mint_secret_name = _split_mint.secret.name == "daemon-secret"
assert_split_mint_vol_name = _split_mint.volume.name == "daemon-vol"
assert_split_mint_vol_source = _split_mint.volume.source.secret.name == "daemon-secret"
