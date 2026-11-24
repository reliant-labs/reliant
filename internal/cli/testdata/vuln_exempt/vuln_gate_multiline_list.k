import forge

bundle = forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        vuln_gate = forge.VulnPolicy {
            mode = "enforce"
            exemptions = [
                forge.VulnExemption {id = "GHSA-aaaa-bbbb-cccc", reason = "dev only", expires = "2026-12-01"}
            ]
        }
    }
}
