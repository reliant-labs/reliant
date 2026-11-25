"""
Positive test — KCL-native schema INHERITANCE of fw.Workload.

A project may declare `schema BillingService(fw.Workload)` to add its OWN
typed/required fields (a tier enum, a required region) while forge renders
the result exactly like a plain fw.Workload: the app's extra fields ride
along on the typed value and never reach the contract or the record.

The COMPANION negative (extra REQUIRED field enforced at parse time) is
negative_service_inheritance_missing_required.k.
"""

import forge
import forge.workloads as fw

schema BillingService(fw.Workload):
    region: str
    tier: "free" | "pro" = "free"

    check:
        region, "BillingService.region is required"

_target = forge.ClusterTarget {cluster = "prod-cluster", namespace = "billing-prod"}

_svc = BillingService {
    name = "billing-api"
    image = "ghcr.io/reliant-labs/billing-api"
    region = "us-east-1"
    tier = "pro"
    ports = [fw.Port {name = "http", port = 8080}]
    runtime = forge.OnCluster {target = _target}
}
_plain = fw.Workload {
    name = "billing-api"
    image = "ghcr.io/reliant-labs/billing-api"
    ports = [fw.Port {name = "http", port = 8080}]
    runtime = forge.OnCluster {target = _target}
}

_out = forge.render(forge.Bundle {project = "billing", workloads = [_svc]})
_plain_out = forge.render(forge.Bundle {project = "billing", workloads = [_plain]})

assert_one_workload = len(_out.workloads) == 1
# The subtype renders IDENTICALLY to the plain declaration.
assert_same_entity = _out.workloads == _plain_out.workloads
assert_same_manifests = _out.manifests == _plain_out.manifests
# The app's own fields stay on the typed value and out of the wire shape.
assert_extras_on_value = _svc.region == "us-east-1" and _svc.tier == "pro"
assert_extras_not_in_spec = "region" not in _out.workloads[0].spec and "tier" not in _out.workloads[0].spec
