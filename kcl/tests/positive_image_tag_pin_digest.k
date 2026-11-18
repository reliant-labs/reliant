"""
Positive test — digest pinning for an image that pins its OWN TAG.

THE DEFECT THIS PINS. A workload declaring `image = "localhost:5051/reliant:e2e"` is still
an image forge BUILDS and PUSHES: the build records
`{"image":"reliant","tag":"e2e","digest":"sha256:…"}`. But the lowering used
to return a tag-pinned image verbatim and never consult `image_digests` at
all, so a rebuild that pushed new bytes under the same tag left
`spec.image = "localhost:5051/reliant:e2e"` UNCHANGED. An unchanged
Deployment spec is not a rollout, and with `imagePullPolicy: IfNotPresent`
the nodes kept running the previous digest. The deploy reported success.
That is a silent no-op deploy — exactly the failure digest pinning exists to
prevent.

THE RULE. A tag-pinned image looks itself up VERBATIM (the full declared
reference, `localhost:5051/reliant:e2e`), so a
digest only applies when it was captured for THAT tag. The tag is KEPT for
readability and the digest is appended — `reg/reliant:e2e@sha256:…` — which
the kubelet resolves by digest, so the tag is documentation and the bytes are
pinned.

  * tag-pinned + a digest under `\u003cimage\u003e:\u003ctag\u003e` → reg/img:tag@sha256:…
  * tag-pinned + only an UNTAGGED-repository digest (captured for a DIFFERENT
    tag of the same image) → verbatim reg/img:tag. Pinning it would ship bytes that were
    never pushed under this tag.
  * digest-pinned → verbatim, always.
  * unpinned → the untagged-repository digest, exactly as before.
  * third-party (own registry host), absent from the map → verbatim.
"""

# kcl-args: image_tag="envtag" image_digests="{\"localhost:5051/reliant\":\"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"localhost:5051/reliant:e2e\":\"sha256:b372e3a911bca415e0a2f9358b101a51b72fda0913617f28dbba75e408e64f98\",\"localhost:5051/workspace-base:dev-per-daemon\":\"sha256:e3864c527826a6c7d7ff5b901dc78dabace8fa499691831a9b265f5957655d3b\",\"localhost:5051/plain\":\"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\"}"

import forge
import forge.workloads as fw

_e2e_digest = "sha256:b372e3a911bca415e0a2f9358b101a51b72fda0913617f28dbba75e408e64f98"
_base_digest = "sha256:e3864c527826a6c7d7ff5b901dc78dabace8fa499691831a9b265f5957655d3b"
_untagged_digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
_plain_digest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

_target = forge.ClusterTarget {cluster = "k3d-cp", namespace = "e2e"}
_shell = forge.ShellBuild {cmd = "echo built"}

output = forge.render(forge.Bundle {
    project = "cp"
    workloads = [_w | {runtime = forge.OnCluster {target = _target}} if not _w.runtime else _w for _w in [
        # The control-plane e2e shape: forge builds these, pushed at :e2e.
        fw.Workload {name = "reliant-api-server", image = "localhost:5051/reliant:e2e", args = ["api"], build = _shell}
        fw.Workload {name = "reliant-temporal-worker", image = "localhost:5051/reliant:e2e", args = ["worker"], build = _shell}
        fw.Workload {name = "workspace-base", image = "localhost:5051/workspace-base:dev-per-daemon", build = _shell}
        # Same bare image, a DIFFERENT tag: the bare-name digest must NOT leak
        # onto it — those bytes were never pushed under :v9.
        fw.Workload {name = "reliant-other-tag", image = "localhost:5051/reliant:v9", args = ["api"]}
        # Unpinned: the bare-name lookup, unchanged.
        fw.Workload {name = "plain", image = "localhost:5051/plain"}
        fw.Workload {name = "no-digest", image = "localhost:5051/no-digest"}
        # Digest-pinned and third-party: verbatim.
        fw.Workload {name = "pinned-digest", image = "ghcr.io/acme/db@sha256:abc123def456abc123def456abc123def456abc123def456abc123def456abcd"}
        fw.Workload {name = "vendor", image = "ghcr.io/berriai/litellm:main-v1.0"}
    ]]
})

_img = {m.metadata.name: m.spec.image for m in output.manifests if m.kind == "Workload"}

# THE FIX: a forge-built, tag-pinned image deploys by digest, tag retained.
assert_api_server_digest_pinned = _img["reliant-api-server"] == "localhost:5051/reliant:e2e@" + _e2e_digest
assert_worker_shares_digest = _img["reliant-temporal-worker"] == "localhost:5051/reliant:e2e@" + _e2e_digest
assert_workspace_base_digest_pinned = _img["workspace-base"] == "localhost:5051/workspace-base:dev-per-daemon@" + _base_digest

# A digest captured for ANOTHER tag of the same image must not be borrowed.
assert_other_tag_not_pinned = _img["reliant-other-tag"] == "localhost:5051/reliant:v9"
assert_other_tag_carries_no_digest = "@" not in _img["reliant-other-tag"]

# Unchanged behaviour for everything else.
assert_unpinned_uses_untagged_digest = _img["plain"] == "localhost:5051/plain@" + _plain_digest
assert_unpinned_no_digest_uses_tag = _img["no-digest"] == "localhost:5051/no-digest:envtag"
assert_digest_pinned_verbatim = _img["pinned-digest"] == \
    "ghcr.io/acme/db@sha256:abc123def456abc123def456abc123def456abc123def456abc123def456abcd"
assert_vendor_verbatim = _img["vendor"] == "ghcr.io/berriai/litellm:main-v1.0"

# The entity record carries the same resolved ref the manifest does.
assert_entity_matches_record = all w in output.workloads { w.spec.image == _img[w.name] }
