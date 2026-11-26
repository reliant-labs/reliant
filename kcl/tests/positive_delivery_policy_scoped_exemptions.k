"""
Positive test — scoped exemptions: an advisory exempted only at one path of
one artifact, beside an unscoped exemption of another advisory and a second
scope of the same advisory. Unscoped exemptions stay in `exemptions`, exactly
as before; scoped ones are recorded in `scoped_exemptions`, sorted by
identity, each scope list sorted, unset scope lists absent.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        vuln_gate = forge.VulnPolicy {
            exemptions = [
                forge.VulnExemption {id = "GO-2026-6859", reason = "grype never parses untrusted archives", expires = "2026-12-31", artifacts = ["api"], locations = ["/usr/local/bin/syft", "/usr/local/bin/grype"]}
                forge.VulnExemption {id = "GO-2026-6859", reason = "the worker never serves http", expires = "2026-12-31", packages = ["pkg:golang/stdlib"], artifacts = ["worker"]}
                forge.VulnExemption {id = "CVE-2026-0001", reason = "fixed in the next base image", expires = "2026-12-01"}
            ]
        }
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

_g = output.delivery_policy.vuln_gate
assert_unscoped_unchanged = _g.exemptions == [{"id": "CVE-2026-0001", "reason": "fixed in the next base image", "expires": "2026-12-01"}]
# Sorted by identity, "<id>|<packages>|<artifacts>|<locations>": the worker's
# entry names a package, so it sorts first.
assert_scoped_sorted = [e.artifacts for e in _g.scoped_exemptions] == [["worker"], ["api"]]
assert_locations_sorted = _g.scoped_exemptions[1].locations == ["/usr/local/bin/grype", "/usr/local/bin/syft"]
assert_unset_scope_absent = "locations" not in _g.scoped_exemptions[0] and "packages" not in _g.scoped_exemptions[1]
assert_package_kept = _g.scoped_exemptions[0].packages == ["pkg:golang/stdlib"]
