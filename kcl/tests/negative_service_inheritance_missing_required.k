"""
Negative test — an app schema that inherits fw.Workload can add its OWN
required fields, enforced at PARSE time.

BillingService(fw.Workload) declares `region` with a `check:` that rejects
an empty value. Constructing it with `region = ""` must fail the check block
— proving the app's extra required-field semantics are enforced when the
value is loaded, exactly like forge's own checks.
"""

# expect: BillingService.region is required

import forge.workloads as fw

schema BillingService(fw.Workload):
    region: str
    tier: "free" | "pro" = "free"

    check:
        region, "BillingService.region is required"

_svc = BillingService {
    name = "billing-api"
    image = "billing-api"
    region = ""
}
