"""
Positive test — PER-IMAGE digest pinning in the cluster lowering.

`forge env deploy` pins each workload's image to ITS OWN content-addressed
digest by passing `-D image_digests='{"<image>":"sha256:…"}'` (map §2.3,
cluster.go renderDArgs). The map is keyed by the workload's DECLARED
reference — registry host included, because that is what was pushed — and the
lowering renders `<image>@sha256:…` in the record's spec.image, dropping the
env tag. A digest cannot go stale or be re-pointed.

REGRESSION guarded: ONE env-wide digest used to be fanned onto every image,
so `reliant-api-server` was pinned to the `control-plane` digest
(`manifest unknown`). Distinct image ⇒ distinct digest; shared image ⇒ the
same digest; no entry ⇒ the env tag; third-party ref ⇒ verbatim.
"""

# kcl-args: image_tag="staging" image_digests="{\"ghcr.io/reliant/control-plane\":\"sha256:d1818c3c2f8a3823f4f5a533a893bd735035fc6b0d79b700aaa8f1d6727e7182\",\"ghcr.io/reliant/reliant\":\"sha256:12ac5c2e15de35da3c47b8d0d23a68aad80e60dd69467bd0a689a0292f91d4e4\"}"

import forge
import forge.workloads as fw

_cp_digest = "sha256:d1818c3c2f8a3823f4f5a533a893bd735035fc6b0d79b700aaa8f1d6727e7182"
_reliant_digest = "sha256:12ac5c2e15de35da3c47b8d0d23a68aad80e60dd69467bd0a689a0292f91d4e4"

_target = forge.ClusterTarget {cluster = "k3d-dev", namespace = "dev"}

output = forge.render(forge.Bundle {
    project = "cp"
    workloads = [_w | {runtime = forge.OnCluster {target = _target}} if not _w.runtime else _w for _w in [
        fw.Workload {name = "control-plane", image = "ghcr.io/reliant/control-plane"}
        fw.Workload {name = "reliant-api-server", image = "ghcr.io/reliant/reliant", args = ["api"]}
        fw.Workload {name = "reliant-temporal-worker", image = "ghcr.io/reliant/reliant", args = ["worker"]}
        fw.Workload {name = "no-digest", image = "ghcr.io/reliant/no-digest"}
        fw.Workload {name = "sidecar", image = "ghcr.io/reliant/sidecar:v9.9.9"}
        fw.Workload {name = "litellm", image = "ghcr.io/berriai/litellm:main-v1.0"}
    ]]
})

_img = {m.metadata.name: m.spec.image for m in output.manifests if m.kind == "Workload"}

assert_cp_digest = _img["control-plane"] == "ghcr.io/reliant/control-plane@" + _cp_digest
# THE regression: a distinct image must not inherit another image's digest.
assert_reliant_api_digest = _img["reliant-api-server"] == "ghcr.io/reliant/reliant@" + _reliant_digest
assert_reliant_worker_shares_digest = _img["reliant-temporal-worker"] == "ghcr.io/reliant/reliant@" + _reliant_digest
assert_distinct = _img["control-plane"] != _img["reliant-api-server"]
assert_nodigest_fallback = _img["no-digest"] == "ghcr.io/reliant/no-digest:staging"
assert_pinned_wins = _img["sidecar"] == "ghcr.io/reliant/sidecar:v9.9.9"
assert_vendor_passthrough = _img["litellm"] == "ghcr.io/berriai/litellm:main-v1.0"
